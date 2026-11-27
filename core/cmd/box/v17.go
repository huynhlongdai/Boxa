package main

// v1.7 CLI: `box app …` (supervised app) and `box url … --token` (scoped URL tokens).

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"boxmes/core/internal/client"
	"boxmes/core/internal/manager"
	"boxmes/core/internal/model"
	"boxmes/core/internal/proto"
)

func cmdURLToken(ctx context.Context, c *client.Client, box, portS string, issue bool, ttl time.Duration, label string, list bool, revoke string, public bool) error {
	port, err := strconv.Atoi(portS)
	if err != nil {
		return fmt.Errorf("port must be a number: %q", portS)
	}
	base := "/boxes/" + url.PathEscape(box) + "/ports/" + strconv.Itoa(port)
	switch {
	case public:
		var v manager.PortView
		if err := c.JSON(ctx, "PUT", base, map[string]any{"access": "public"}, &v); err != nil {
			return err
		}
		fmt.Printf("%d\t%s\tpublic\n", v.Port, v.URL)
		return nil
	case revoke != "":
		var t model.URLToken
		if err := c.JSON(ctx, "DELETE", base+"/tokens/"+url.PathEscape(revoke), nil, &t); err != nil {
			return err
		}
		fmt.Printf("revoked %s (%s…)\n", t.ID, t.Prefix)
		return nil
	case list:
		var l manager.URLTokenList
		if err := c.JSON(ctx, "GET", base+"/tokens", nil, &l); err != nil {
			return err
		}
		fmt.Printf("port %d access=%s %s\n", l.Port, l.Access, l.GatewayURL)
		for _, t := range l.Items {
			state := "active"
			switch {
			case t.RevokedAt != nil:
				state = "revoked"
			case t.Expired:
				state = "expired"
			}
			fmt.Printf("%s\t%s…\t%s\texpires %s\t%s\n", t.ID, t.Prefix, state, t.ExpiresAt.Local().Format(time.RFC3339), t.Label)
		}
		return nil
	}
	var t model.URLToken
	if err := c.JSON(ctx, "POST", base+"/tokens", map[string]any{"ttl_s": int(ttl.Seconds()), "label": label}, &t); err != nil {
		return err
	}
	if t.URL != "" {
		fmt.Println(t.URL)
	}
	fmt.Printf("token %s (id %s, expires %s) — send it as ?token=, Authorization: Bearer or X-Boxmes-Token\n", t.Token, t.ID, t.ExpiresAt.Local().Format(time.RFC3339))
	return nil
}

func printApp(v model.AppView) {
	st := v.Status
	fmt.Printf("state\t%s\nready\t%v\nrestarts\t%d\n", st.State, st.Ready, st.Restarts)
	if st.LastExit != nil {
		fmt.Printf("last_exit\t%d\n", *st.LastExit)
	}
	if st.StartedAt != nil {
		fmt.Printf("started\t%s\n", st.StartedAt.Local().Format(time.RFC3339))
	}
	if st.Supervisor != "" {
		fmt.Printf("supervisor\t%s\n", st.Supervisor)
	}
	if st.Message != "" {
		fmt.Printf("message\t%s\n", st.Message)
	}
	if v.Spec != nil {
		fmt.Printf("start\t%s\n", v.Spec.Start)
		if v.Spec.Port > 0 {
			fmt.Printf("port\t%d\n", v.Spec.Port)
		}
	}
}

func cmdApp(ctx context.Context, c *client.Client, args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("usage: box app set|status|restart|logs|rm <box> …")
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "set":
		fs := flag.NewFlagSet("app set", flag.ContinueOnError)
		start := fs.String("start", "", "start command")
		repo := fs.String("repo", "", "git URL")
		ref := fs.String("ref", "", "branch or tag")
		install := fs.String("install", "", "install command")
		port := fs.Int("port", 0, "port the app listens on")
		readyHTTP := fs.String("ready-http", "", "readiness path")
		readyTCP := fs.Bool("ready-tcp", false, "tcp readiness check")
		restart := fs.String("restart", "", "always|on-failure|never")
		workdir := fs.String("workdir", "", "box path")
		var envs multi
		fs.Var(&envs, "env", "K=V (repeatable)")
		if err := fs.Parse(reorder(fs, rest)); err != nil {
			return err
		}
		if fs.NArg() < 1 {
			return fmt.Errorf("usage: box app set <box> --start \"npm start\" [--port 8000] …")
		}
		spec := model.AppSpec{Start: *start, Repo: *repo, Ref: *ref, Install: *install, Port: *port, Restart: *restart, Workdir: *workdir}
		if *readyHTTP != "" {
			spec.Ready = &model.AppReady{HTTP: *readyHTTP}
		} else if *readyTCP {
			spec.Ready = &model.AppReady{TCP: true}
		}
		for _, kv := range envs {
			k, v, ok := strings.Cut(kv, "=")
			if !ok {
				return fmt.Errorf("--env wants K=V, got %q", kv)
			}
			if spec.Env == nil {
				spec.Env = map[string]string{}
			}
			spec.Env[k] = v
		}
		var v model.AppView
		if err := c.JSON(ctx, "PUT", "/boxes/"+url.PathEscape(fs.Arg(0))+"/app", spec, &v); err != nil {
			return err
		}
		printApp(v)
		return nil
	case "status", "restart", "rm":
		var v model.AppView
		p := "/boxes/" + url.PathEscape(rest[0]) + "/app"
		var err error
		switch sub {
		case "status":
			err = c.JSON(ctx, "GET", p, nil, &v)
		case "restart":
			err = c.JSON(ctx, "POST", p+"/restart", map[string]any{}, &v)
		default:
			err = c.JSON(ctx, "DELETE", p, nil, &v)
		}
		if err != nil {
			return err
		}
		printApp(v)
		return nil
	case "logs":
		fs := flag.NewFlagSet("app logs", flag.ContinueOnError)
		follow := fs.Bool("f", false, "follow")
		bytes := fs.Int("bytes", 8000, "bytes of history")
		if err := fs.Parse(reorder(fs, rest)); err != nil {
			return err
		}
		if fs.NArg() < 1 {
			return fmt.Errorf("usage: box app logs <box> [-f]")
		}
		p := "/boxes/" + url.PathEscape(fs.Arg(0)) + "/app/logs?offset=-1&max=" + strconv.Itoa(*bytes)
		if !*follow {
			var r proto.AppLogsResult
			if err := c.JSON(ctx, "GET", p, nil, &r); err != nil {
				return err
			}
			fmt.Print(r.Data)
			return nil
		}
		return followLogs(ctx, c, p+"&follow=1")
	}
	return fmt.Errorf("unknown app command %q (set|status|restart|logs|rm)", sub)
}

// followLogs streams the SSE log feed to stdout until interrupted.
func followLogs(ctx context.Context, c *client.Client, path string) error {
	req, err := http.NewRequestWithContext(ctx, "GET", c.BaseURL+"/v1"+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Key)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 1<<20), 4<<20)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var r proto.AppLogsResult
		if json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &r) == nil {
			os.Stdout.WriteString(r.Data)
		}
	}
	return sc.Err()
}
