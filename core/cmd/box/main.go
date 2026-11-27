// box: Boxmes CLI. Talks to core over REST (BOXMES_URL, BOXMES_API_KEY).
// `box mcp` runs a stdio MCP server exposing the box_* tools, resources and prompts.
package main

import (
	"boxmes/core/internal/envcompat"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"boxmes/core/internal/boxyaml"
	"boxmes/core/internal/client"
	"boxmes/core/internal/i18n"
	"boxmes/core/internal/manager"
	"boxmes/core/internal/mcp"
	"boxmes/core/internal/model"
)

const usage = `box — Boxmes CLI

Env: BOXMES_URL (default http://localhost:8080), BOXMES_API_KEY, BOXMES_LANG (vi|en|zh|ja; default from LANG)

  box up [--template python] [--lifecycle task|session|service] [--cpu 1] [--ram 1G] [--disk 10G]
         [--ttl 1h] [--idle 5] [--budget 0.5] [--slug name] [--port 8080] [--secret NAME]
         [--setup "cmd"] [-f box.yaml] [--idempotency-key k] [--no-wait]
  box exec <box> [--async] [--timeout 300] [--cwd /work] [--tail 60] [--json] -- <command>
  box ls [--all]
  box read <box> <path> [--from N] [--to M]
  box write <box> <path> [<local-file>|-]      (stdin when no file or "-")
  box grep <box> <pattern> [path]
  box sleep|wake|snapshot|down <box>
  box job <job_id> [--wait 30]
  box log <log_id>
  box cost [<box>]
  box quote [same flags as up]                  (dry-run price, nothing created)
  box batch <box> [--keep-going] -- <cmd1> ;; <cmd2> ;; ...
  box restore <box> [snapshot_id]
  box fork <box> [snapshot_id] [--slug name]
  box clone <box> [--slug name]                 (sandbox boxes: provider-native fork when available)
  box url <box> [port]                          (public URL of a port; with a port, exposes it)
  box url <box> [port] --name demo-app [--as api]  (readable URL: https://demo-app.<domain>/, port label → demo-app-api)
  box url <box> <port> --token [--ttl 24h] [--label who]   (scoped URL token; the port then needs it)
  box url <box> <port> --tokens | --revoke <token_id> | --public
  box app set <box> --start "npm start" [--repo URL] [--ref main] [--install "npm ci"] [--port 8000]
         [--ready-http /healthz | --ready-tcp] [--restart always|on-failure|never] [--env K=V] [--workdir /work/app]
  box app status|restart|rm <box>
  box app logs <box> [-f] [--bytes 8000]
  box yaml <box>
` + accessUsage + `  box mcp                                       (stdio MCP server)
`

type multi []string

func (m *multi) String() string     { return strings.Join(*m, ",") }
func (m *multi) Set(v string) error { *m = append(*m, v); return nil }

// reorder moves flags before positionals (until "--") so `box exec b1 --async -- cmd` works.
func reorder(fs *flag.FlagSet, args []string) []string {
	var flags, pos []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			pos = append(pos, args[i:]...)
			break
		}
		if strings.HasPrefix(a, "-") && len(a) > 1 {
			flags = append(flags, a)
			name := strings.TrimLeft(a, "-")
			if strings.Contains(name, "=") {
				continue
			}
			if f := fs.Lookup(name); f != nil {
				if bf, ok := f.Value.(interface{ IsBoolFlag() bool }); ok && bf.IsBoolFlag() {
					continue
				}
				if i+1 < len(args) {
					flags = append(flags, args[i+1])
					i++
				}
			}
			continue
		}
		pos = append(pos, a)
	}
	return append(flags, pos...)
}

func die(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}

func printJSON(v any) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	enc.Encode(v)
}

func main() {
	if n := envcompat.Note(envcompat.Apply()); n != "" {
		fmt.Fprintln(os.Stderr, "box: warning: "+n)
	}
	if len(os.Args) < 2 {
		fmt.Print(usage)
		os.Exit(2)
	}
	c := client.New(envOr("BOXMES_URL", "http://localhost:8080"), os.Getenv("BOXMES_API_KEY"))
	c.Lang = i18n.Normalize(os.Getenv("BOXMES_LANG"))
	if c.Lang == "" {
		c.Lang = i18n.Normalize(os.Getenv("LANG"))
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	cmd, args := os.Args[1], os.Args[2:]
	var err error
	switch cmd {
	case "up":
		err = cmdUp(ctx, c, args)
	case "exec":
		err = cmdExec(ctx, c, args)
	case "ls", "list":
		err = cmdLs(ctx, c, args)
	case "read", "cat":
		err = cmdRead(ctx, c, args)
	case "write":
		err = cmdWrite(ctx, c, args)
	case "grep":
		err = cmdGrep(ctx, c, args)
	case "sleep", "wake", "snapshot":
		err = cmdVerb(ctx, c, cmd, args)
	case "down", "rm":
		err = cmdDown(ctx, c, args)
	case "job":
		err = cmdJob(ctx, c, args)
	case "log":
		err = cmdLog(ctx, c, args)
	case "cost":
		err = cmdCost(ctx, c, args)
	case "quote":
		err = cmdQuote(ctx, c, args)
	case "batch":
		err = cmdBatch(ctx, c, args)
	case "restore", "fork", "clone":
		err = cmdRestoreFork(ctx, c, cmd, args)
	case "url", "ports":
		err = cmdURL(ctx, c, args)
	case "yaml":
		err = cmdYAML(ctx, c, args)
	case "access":
		err = cmdAccess(ctx, c, args)
	case "ticket":
		err = cmdTicket(ctx, c, args)
	case "forward":
		err = cmdForward(ctx, c, args)
	case "ssh":
		err = cmdSSH(ctx, c, args)
	case "ssh-proxy":
		err = cmdSSHProxy(ctx, c, args)
	case "ssh-config":
		err = cmdSSHConfig(ctx, c, args)
	case "ssh-key":
		err = cmdSSHKey(ctx, c, args)
	case "ssh-cert":
		err = cmdSSHCert(ctx, c, args)
	case "sftp", "scp":
		err = cmdSFTP(ctx, c, args)
	case "browser", "cdp":
		err = cmdBrowser(ctx, c, args)
	case "desktop", "vnc":
		err = cmdDesktop(ctx, c, args)
	case "terminals", "terminal":
		err = cmdTerminals(ctx, c, args)
	case "app":
		err = cmdApp(ctx, c, args)
	case "mcp":
		s := &mcp.Server{Name: "boxmes", Version: manager.Version}
		if c.Lang != "" {
			ctx = i18n.WithLocale(ctx, c.Lang)
		}
		err = s.ServeStdio(ctx, c, os.Stdin, os.Stdout)
	case "version":
		fmt.Println("box", manager.Version)
	case "help", "-h", "--help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", cmd, usage)
		os.Exit(2)
	}
	if err != nil {
		die(err)
	}
}

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func cmdUp(ctx context.Context, c *client.Client, args []string) error {
	return upOrQuote(ctx, c, args, false)
}

func cmdQuote(ctx context.Context, c *client.Client, args []string) error {
	return upOrQuote(ctx, c, args, true)
}

func upOrQuote(ctx context.Context, c *client.Client, args []string, quote bool) error {
	fs := flag.NewFlagSet("up", flag.ExitOnError)
	tmpl := fs.String("template", "base", "template")
	lc := fs.String("lifecycle", "session", "task|session|service")
	cpu := fs.Float64("cpu", 0, "vCPUs")
	ram := fs.String("ram", "", "RAM e.g. 1G or 512M")
	disk := fs.String("disk", "", "disk e.g. 10G")
	ttl := fs.String("ttl", "", "TTL e.g. 1h (task)")
	idle := fs.Int("idle", 0, "idle minutes before sleep (session)")
	budget := fs.Float64("budget", 0, "budget USD")
	slug := fs.String("slug", "", "name")
	file := fs.String("f", "", "box.yaml path")
	idem := fs.String("idempotency-key", "", "idempotency key")
	noWait := fs.Bool("no-wait", false, "return immediately")
	jsonOut := fs.Bool("json", false, "print JSON")
	var ports, secrets, setup multi
	fs.Var(&ports, "port", "port to expose (repeatable)")
	fs.Var(&secrets, "secret", "secret name (repeatable)")
	fs.Var(&setup, "setup", "setup command (repeatable)")
	fs.Parse(reorder(fs, args))
	body := map[string]any{}
	if *file != "" {
		b, err := os.ReadFile(*file)
		if err != nil {
			return err
		}
		if _, errs := boxyaml.Parse(string(b)); errs != nil {
			for _, e := range errs {
				fmt.Fprintf(os.Stderr, "%s:%d: %s\n", *file, e.Line, e.Message)
			}
			return fmt.Errorf("invalid box.yaml")
		}
		body["yaml"] = string(b)
	} else {
		body["template"], body["lifecycle"] = *tmpl, *lc
		spec := map[string]any{}
		if *cpu > 0 {
			spec["cpu"] = *cpu
		}
		if *ram != "" {
			mb, err := boxyaml.ParseSize(*ram, 1)
			if err != nil {
				return err
			}
			spec["ram_mb"] = mb
		}
		if *disk != "" {
			mb, err := boxyaml.ParseSize(*disk, 1024)
			if err != nil {
				return err
			}
			spec["disk_gb"] = mb / 1024
		}
		body["spec"] = spec
		if *ttl != "" {
			d, err := boxyaml.ParseDuration(*ttl)
			if err != nil {
				return err
			}
			body["ttl_seconds"] = int(d.Seconds())
		}
		if *idle > 0 {
			body["idle_minutes"] = *idle
		}
		if *budget > 0 {
			body["budget_usd"] = *budget
		}
		if *slug != "" {
			body["slug"] = *slug
		}
		var ps []int
		for _, p := range ports {
			n, err := strconv.Atoi(p)
			if err != nil {
				return fmt.Errorf("bad port %q", p)
			}
			ps = append(ps, n)
		}
		if len(ps) > 0 {
			body["ports"] = ps
		}
		if len(secrets) > 0 {
			body["secrets"] = []string(secrets)
		}
		if len(setup) > 0 {
			body["setup"] = []string(setup)
		}
	}
	if quote {
		var q map[string]any
		if err := c.JSON(ctx, "POST", "/quote", body, &q); err != nil {
			return err
		}
		if *jsonOut {
			printJSON(q)
			return nil
		}
		if q["available"] == true {
			fmt.Printf("%v/%v  %s/h  ≈ %s total for %vh  within_budget=%v\n", q["provider"], q["region"], fmtNum(q["cost_per_hour"]), fmtNum(q["expected_total_usd"]), q["expected_hours"], q["within_budget"])
		} else {
			fmt.Printf("not available now: %v\n", q["placement_reason"])
		}
		if alts, _ := q["alternatives"].([]any); len(alts) > 0 {
			for _, a := range alts {
				m, _ := a.(map[string]any)
				fmt.Printf("  alt: %v %v %v %s/h ≈ %s\n", m["kind"], m["provider"], m["region"], fmtNum(m["cost_per_hour"]), fmtNum(m["expected_total_usd"]))
			}
		}
		return nil
	}
	if *idem != "" {
		body["idempotency_key"] = *idem
	}
	path := "/boxes"
	if *noWait {
		path += "?wait=false"
	}
	var box map[string]any
	if err := c.JSON(ctx, "POST", path, body, &box); err != nil {
		return err
	}
	if *jsonOut {
		printJSON(box)
		return nil
	}
	fmt.Printf("%s  %s  %v  %s/%s  %s/h\n", box["id"], box["slug"], box["status"], box["provider"], box["host_id"], fmtNum(box["cost_per_hour"]))
	if r, _ := box["placement_reason"].(string); r != "" {
		fmt.Printf("  placement: %s\n", r)
	}
	if e, _ := box["error"].(string); e != "" {
		fmt.Printf("  error: %s\n", e)
	}
	if box["status"] == "failed" {
		os.Exit(1)
	}
	return nil
}

func fmtNum(v any) string {
	if f, ok := v.(float64); ok {
		return "$" + strconv.FormatFloat(f, 'f', 4, 64)
	}
	return fmt.Sprint(v)
}

func cmdExec(ctx context.Context, c *client.Client, args []string) error {
	fs := flag.NewFlagSet("exec", flag.ExitOnError)
	async := fs.Bool("async", false, "run as background job")
	timeout := fs.Int("timeout", 0, "timeout seconds")
	cwd := fs.String("cwd", "", "working dir")
	tail := fs.Int("tail", 0, "tail lines")
	jsonOut := fs.Bool("json", false, "print raw JSON")
	fs.Parse(reorder(fs, args))
	rest := fs.Args()
	if len(rest) < 2 {
		return fmt.Errorf("usage: box exec <box> -- <command>")
	}
	box := rest[0]
	cmdArgs := rest[1:]
	if cmdArgs[0] == "--" {
		cmdArgs = cmdArgs[1:]
	}
	body := map[string]any{"command": strings.Join(cmdArgs, " "), "async": *async}
	if *timeout > 0 {
		body["timeout_s"] = *timeout
	}
	if *cwd != "" {
		body["cwd"] = *cwd
	}
	if *tail > 0 {
		body["tail_lines"] = *tail
	}
	var res map[string]any
	if err := c.JSON(ctx, "POST", "/boxes/"+url.PathEscape(box)+"/exec", body, &res); err != nil {
		return err
	}
	if *jsonOut || *async {
		printJSON(res)
		return nil
	}
	if s, _ := res["stdout_tail"].(string); s != "" {
		fmt.Println(s)
	}
	if s, _ := res["stderr_tail"].(string); s != "" {
		fmt.Fprintln(os.Stderr, s)
	}
	if t, _ := res["truncated"].(bool); t {
		fmt.Fprintf(os.Stderr, "[boxmes] output truncated; full log: box log %v\n", res["log_id"])
	}
	code := int(res["exit_code"].(float64))
	if code != 0 {
		os.Exit(code)
	}
	return nil
}

func cmdLs(ctx context.Context, c *client.Client, args []string) error {
	fs := flag.NewFlagSet("ls", flag.ExitOnError)
	all := fs.Bool("all", false, "include terminated")
	jsonOut := fs.Bool("json", false, "JSON")
	fs.Parse(reorder(fs, args))
	path := "/boxes"
	if *all {
		path += "?all=true"
	}
	var res struct {
		Items []map[string]any `json:"items"`
	}
	if err := c.JSON(ctx, "GET", path, nil, &res); err != nil {
		return err
	}
	if *jsonOut {
		printJSON(res)
		return nil
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tSLUG\tSTATUS\tLIFECYCLE\tAGENT\tPROVIDER\tSPENT\t$/H")
	for _, b := range res.Items {
		fmt.Fprintf(tw, "%v\t%v\t%v\t%v\t%v\t%v\t%s\t%s\n", b["id"], b["slug"], b["status"], b["lifecycle"], b["agent_name"], b["provider"], fmtNum(b["spent_usd"]), fmtNum(b["cost_per_hour"]))
	}
	return tw.Flush()
}

func cmdRead(ctx context.Context, c *client.Client, args []string) error {
	fs := flag.NewFlagSet("read", flag.ExitOnError)
	from := fs.Int("from", 0, "first line")
	to := fs.Int("to", 0, "last line")
	fs.Parse(reorder(fs, args))
	if fs.NArg() < 2 {
		return fmt.Errorf("usage: box read <box> <path>")
	}
	q := url.Values{"path": {fs.Arg(1)}}
	if *from > 0 {
		q.Set("from", strconv.Itoa(*from))
	}
	if *to > 0 {
		q.Set("to", strconv.Itoa(*to))
	}
	var res map[string]any
	if err := c.JSON(ctx, "GET", "/boxes/"+url.PathEscape(fs.Arg(0))+"/files?"+q.Encode(), nil, &res); err != nil {
		return err
	}
	if res["type"] == "dir" {
		ents, _ := res["entries"].([]any)
		for _, e := range ents {
			m := e.(map[string]any)
			suffix := ""
			if m["type"] == "dir" {
				suffix = "/"
			}
			fmt.Printf("%10v  %v%s\n", m["size"], m["name"], suffix)
		}
		return nil
	}
	fmt.Print(res["content"])
	return nil
}

func cmdWrite(ctx context.Context, c *client.Client, args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("usage: box write <box> <path> [<local-file>|-]")
	}
	var data []byte
	var err error
	if len(args) >= 3 && args[2] != "-" {
		data, err = os.ReadFile(args[2])
	} else {
		data, err = io.ReadAll(os.Stdin)
	}
	if err != nil {
		return err
	}
	var res map[string]any
	if err := c.JSON(ctx, "PUT", "/boxes/"+url.PathEscape(args[0])+"/files", map[string]any{"path": args[1], "content": string(data)}, &res); err != nil {
		return err
	}
	fmt.Printf("wrote %v bytes to %s\n", res["bytes"], args[1])
	return nil
}

func cmdGrep(ctx context.Context, c *client.Client, args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("usage: box grep <box> <pattern> [path]")
	}
	path := ""
	if len(args) > 2 {
		path = args[2]
	}
	var res struct {
		Matches []struct {
			Path string `json:"path"`
			Line int    `json:"line"`
			Text string `json:"text"`
		} `json:"matches"`
		Truncated bool `json:"truncated"`
	}
	if err := c.JSON(ctx, "POST", "/boxes/"+url.PathEscape(args[0])+"/grep", map[string]any{"pattern": args[1], "path": path}, &res); err != nil {
		return err
	}
	for _, m := range res.Matches {
		fmt.Printf("%s:%d:%s\n", m.Path, m.Line, m.Text)
	}
	if res.Truncated {
		fmt.Fprintln(os.Stderr, "[boxmes] more matches truncated")
	}
	return nil
}

func cmdVerb(ctx context.Context, c *client.Client, verb string, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: box %s <box>", verb)
	}
	var res map[string]any
	if err := c.JSON(ctx, "POST", "/boxes/"+url.PathEscape(args[0])+"/"+verb, map[string]any{}, &res); err != nil {
		return err
	}
	if verb == "snapshot" {
		fmt.Printf("%v  %v MB  %v\n", res["id"], res["size_mb"], res["location"])
		return nil
	}
	fmt.Printf("%v  %v\n", res["id"], res["status"])
	return nil
}

func cmdDown(ctx context.Context, c *client.Client, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: box down <box>")
	}
	var res map[string]any
	if err := c.JSON(ctx, "DELETE", "/boxes/"+url.PathEscape(args[0]), nil, &res); err != nil {
		return err
	}
	fmt.Printf("%v  %v  spent %s\n", res["id"], res["status"], fmtNum(res["spent_usd"]))
	return nil
}

func cmdJob(ctx context.Context, c *client.Client, args []string) error {
	fs := flag.NewFlagSet("job", flag.ExitOnError)
	wait := fs.Int("wait", 0, "seconds to wait")
	fs.Parse(reorder(fs, args))
	if fs.NArg() < 1 {
		return fmt.Errorf("usage: box job <job_id>")
	}
	var res json.RawMessage
	p := "/jobs/" + url.PathEscape(fs.Arg(0))
	if *wait > 0 {
		p += "?wait=" + strconv.Itoa(*wait)
	}
	if err := c.JSON(ctx, "GET", p, nil, &res); err != nil {
		return err
	}
	var v any
	json.Unmarshal(res, &v)
	printJSON(v)
	return nil
}

func cmdLog(ctx context.Context, c *client.Client, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: box log <log_id>")
	}
	st, b, err := c.Do(ctx, "GET", "/logs/"+url.PathEscape(args[0]), nil)
	if err != nil {
		return err
	}
	if st >= 300 {
		return fmt.Errorf("%s", b)
	}
	os.Stdout.Write(b)
	return nil
}

func cmdCost(ctx context.Context, c *client.Client, args []string) error {
	if len(args) > 0 {
		var b map[string]any
		if err := c.JSON(ctx, "GET", "/boxes/"+url.PathEscape(args[0]), nil, &b); err != nil {
			return err
		}
		fmt.Printf("%v (%v): spent %s, %s/h, budget %v\n", b["slug"], b["status"], fmtNum(b["spent_usd"]), fmtNum(b["cost_per_hour"]), b["budget_usd"])
		return nil
	}
	var o map[string]any
	if err := c.JSON(ctx, "GET", "/overview", nil, &o); err != nil {
		return err
	}
	fmt.Printf("today  %s / budget %s\nmonth  %s / budget %s\nboxes  %v running, %v sleeping\nsaved vs 24/7  %s\n",
		fmtNum(o["spent_today"]), fmtNum(o["budget_day"]), fmtNum(o["spent_month"]), fmtNum(o["budget_month"]), o["boxes_running"], o["boxes_sleeping"], fmtNum(o["saved_vs_247_usd"]))
	return nil
}

func cmdBatch(ctx context.Context, c *client.Client, args []string) error {
	fs := flag.NewFlagSet("batch", flag.ExitOnError)
	keep := fs.Bool("keep-going", false, "do not stop at the first failing command")
	tail := fs.Int("tail", 0, "lines per command")
	var before, after []string
	for i, a := range args {
		if a == "--" {
			before, after = args[:i], args[i+1:]
			break
		}
	}
	if after == nil {
		return fmt.Errorf("usage: box batch <box> [--keep-going] -- <cmd1> ;; <cmd2>")
	}
	fs.Parse(reorder(fs, before))
	if fs.NArg() < 1 {
		return fmt.Errorf("usage: box batch <box> -- <cmd1> ;; <cmd2>")
	}
	var cmds []string
	for _, part := range strings.Split(strings.Join(after, " "), ";;") {
		if p := strings.TrimSpace(part); p != "" {
			cmds = append(cmds, p)
		}
	}
	body := map[string]any{"commands": cmds, "stop_on_error": !*keep}
	if *tail > 0 {
		body["tail_lines"] = *tail
	}
	var res struct {
		OK      bool `json:"ok"`
		Results []struct {
			Index      int    `json:"index"`
			Command    string `json:"command"`
			ExitCode   int    `json:"exit_code"`
			StdoutTail string `json:"stdout_tail"`
			StderrTail string `json:"stderr_tail"`
		} `json:"results"`
	}
	if err := c.JSON(ctx, "POST", "/boxes/"+url.PathEscape(fs.Arg(0))+"/exec-batch", body, &res); err != nil {
		return err
	}
	for _, r := range res.Results {
		fmt.Printf("[%d] $ %s  (exit %d)\n", r.Index, r.Command, r.ExitCode)
		if r.StdoutTail != "" {
			fmt.Println(r.StdoutTail)
		}
		if r.StderrTail != "" {
			fmt.Fprintln(os.Stderr, r.StderrTail)
		}
	}
	if !res.OK {
		os.Exit(1)
	}
	return nil
}

func cmdRestoreFork(ctx context.Context, c *client.Client, verb string, args []string) error {
	fs := flag.NewFlagSet(verb, flag.ExitOnError)
	slug := fs.String("slug", "", "name of the forked box")
	fs.Parse(reorder(fs, args))
	if fs.NArg() < 1 {
		return fmt.Errorf("usage: box %s <box> [snapshot_id]", verb)
	}
	body := map[string]any{}
	if fs.NArg() > 1 {
		body["snapshot_id"] = fs.Arg(1)
	}
	if *slug != "" {
		body["slug"] = *slug
	}
	var b map[string]any
	if err := c.JSON(ctx, "POST", "/boxes/"+url.PathEscape(fs.Arg(0))+"/"+verb, body, &b); err != nil {
		return err
	}
	fmt.Printf("%v  %v  %v\n", b["id"], b["slug"], b["status"])
	if u, _ := b["url"].(string); u != "" {
		fmt.Println(u)
	}
	return nil
}

// cmdURL prints the public URL of each port (POST exposes a new port).
func cmdURL(ctx context.Context, c *client.Client, args []string) error {
	fs := flag.NewFlagSet("url", flag.ContinueOnError)
	tok := fs.Bool("token", false, "issue a scoped URL token for the port")
	ttl := fs.Duration("ttl", 24*time.Hour, "token lifetime")
	label := fs.String("label", "", "with --token: note for the token; otherwise: the port's URL label (<name>-<label>.<domain>)")
	as := fs.String("as", "", "the port's URL label, e.g. api → https://<name>-api.<domain>/")
	name := fs.String("name", "", "rename the box's gateway address, e.g. demo-app → https://demo-app.<domain>/")
	list := fs.Bool("tokens", false, "list the port's tokens")
	revoke := fs.String("revoke", "", "revoke a token id")
	public := fs.Bool("public", false, "make the port public again")
	if err := fs.Parse(reorder(fs, args)); err != nil {
		return err
	}
	args = fs.Args()
	if len(args) < 1 {
		return fmt.Errorf("usage: box url <box> [port] [--name <box-name>] [--as <label>] [--token --ttl 24h]")
	}
	if *tok || *list || *revoke != "" || *public {
		if len(args) < 2 {
			return fmt.Errorf("usage: box url <box> <port> --token|--tokens|--revoke <id>|--public")
		}
		return cmdURLToken(ctx, c, args[0], args[1], *tok, *ttl, *label, *list, *revoke, *public)
	}
	var res manager.PortsResult
	p := "/boxes/" + url.PathEscape(args[0]) + "/ports"
	var err error
	if *name != "" {
		var b model.Box
		if err := c.JSON(ctx, "PATCH", "/boxes/"+url.PathEscape(args[0]), map[string]any{"subdomain": *name}, &b); err != nil {
			return err
		}
		fmt.Printf("name: %s\n", b.Subdomain)
	}
	portLabel := *as
	if portLabel == "" {
		portLabel = *label
	}
	if len(args) > 1 {
		port, perr := strconv.Atoi(args[1])
		if perr != nil {
			return fmt.Errorf("port must be a number: %q", args[1])
		}
		body := map[string]any{"port": port}
		if portLabel != "" {
			body["label"] = portLabel
		}
		err = c.JSON(ctx, "POST", p, body, &res)
	} else {
		err = c.JSON(ctx, "GET", p, nil, &res)
	}
	if err != nil {
		return err
	}
	if len(res.Ports) == 0 {
		fmt.Println("no ports exposed; run: box url " + args[0] + " <port>")
		return nil
	}
	for i, v := range res.Ports {
		if len(args) > 1 && i > 0 {
			break
		}
		u := v.URL
		if u == "" {
			u = "-"
		}
		pub := "private"
		if v.Public {
			pub = "public"
		}
		lbl := v.Label
		if v.Primary {
			lbl += "*"
		}
		if lbl == "" {
			lbl = "-"
		}
		fmt.Printf("%d\t%s\t%s\t%s\t%s", v.Port, lbl, u, v.Kind, pub)
		if v.Note != "" && v.URL == "" {
			fmt.Printf("\t%s", v.Note)
		}
		fmt.Println()
		if v.GatewayURL != "" && v.GatewayURL != v.URL {
			fmt.Printf("  gateway (wakes on request): %s\n", v.GatewayURL)
		}
		if v.AliasURL != "" {
			fmt.Printf("  alias: %s\n", v.AliasURL)
		}
		for _, c := range v.Caveats {
			fmt.Println("  ! " + c)
		}
	}
	return nil
}

func cmdYAML(ctx context.Context, c *client.Client, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: box yaml <box>")
	}
	var res map[string]any
	if err := c.JSON(ctx, "GET", "/boxes/"+url.PathEscape(args[0])+"/yaml", nil, &res); err != nil {
		return err
	}
	fmt.Print(res["yaml"])
	return nil
}

var _ = time.Second
