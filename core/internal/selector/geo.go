package selector

import (
	"math"
	"strconv"
	"strings"
)

type ll struct{ lat, lon float64 }

// regionGeo: approximate coordinates of provider regions (by region id) and
// of countries (ISO code), used to estimate round-trip latency without
// probing. Unknown ids → latency unknown (-1).
var regionGeo = map[string]ll{
	// Hetzner
	"fsn1": {50.48, 12.37}, "nbg1": {49.45, 11.08}, "hel1": {60.17, 24.94}, "ash": {39.04, -77.49}, "hil": {45.52, -122.99}, "sin": {1.35, 103.82},
	// Vultr
	"ewr": {40.69, -74.17}, "ord": {41.88, -87.63}, "dfw": {32.78, -96.8}, "sea": {47.61, -122.33}, "lax": {34.05, -118.24}, "atl": {33.75, -84.39},
	"ams": {52.37, 4.9}, "lhr": {51.51, -0.13}, "fra": {50.11, 8.68}, "cdg": {48.86, 2.35}, "nrt": {35.68, 139.69}, "icn": {37.57, 126.98},
	"sgp": {1.35, 103.82}, "syd": {-33.87, 151.21}, "sao": {-23.55, -46.63}, "mia": {25.76, -80.19}, "sjc": {37.34, -121.89}, "waw": {52.23, 21.01},
	"mad": {40.42, -3.7}, "sto": {59.33, 18.07}, "bom": {19.08, 72.88}, "del": {28.61, 77.21}, "blr": {12.97, 77.59}, "jnb": {-26.2, 28.05},
	"mex": {19.43, -99.13}, "yto": {43.65, -79.38}, "tlv": {32.09, 34.78}, "osa": {34.69, 135.5}, "mel": {-37.81, 144.96}, "hnl": {21.31, -157.86},
	// DigitalOcean
	"nyc1": {40.71, -74.0}, "nyc2": {40.71, -74.0}, "nyc3": {40.71, -74.0}, "sfo1": {37.77, -122.42}, "sfo2": {37.77, -122.42}, "sfo3": {37.77, -122.42},
	"ams2": {52.37, 4.9}, "ams3": {52.37, 4.9}, "sgp1": {1.35, 103.82}, "lon1": {51.51, -0.13}, "fra1": {50.11, 8.68}, "tor1": {43.65, -79.38},
	"blr1": {12.97, 77.59}, "syd1": {-33.87, 151.21},
	// Contabo
	"eu": {49.45, 11.08}, "us-central": {32.78, -96.8}, "us-east": {38.9, -77.04}, "us-west": {47.61, -122.33}, "uk": {51.51, -0.13},
	"aus": {-33.87, 151.21}, "jpn": {35.68, 139.69}, "ind": {19.08, 72.88},
	// netcup
	"nue": {49.45, 11.08}, "vie": {48.21, 16.37},
	// countries
	"vn": {10.82, 106.63}, "us": {39.83, -98.58}, "de": {51.17, 10.45}, "sg": {1.35, 103.82}, "jp": {35.68, 139.69}, "cn": {31.23, 121.47},
	"kr": {37.57, 126.98}, "fr": {48.86, 2.35}, "gb": {51.51, -0.13}, "nl": {52.37, 4.9}, "in": {19.08, 72.88}, "au": {-33.87, 151.21},
	"br": {-23.55, -46.63}, "ca": {43.65, -79.38}, "th": {13.76, 100.5}, "id": {-6.21, 106.85}, "my": {3.14, 101.69}, "ph": {14.6, 120.98},
	"hk": {22.32, 114.17}, "tw": {25.03, 121.57}, "fi": {60.17, 24.94}, "pl": {52.23, 21.01}, "es": {40.42, -3.7}, "se": {59.33, 18.07},
}

// Coord resolves a region id, ISO country code or "lat,lon".
func Coord(s string) (float64, float64, bool) {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return 0, 0, false
	}
	if p, ok := regionGeo[s]; ok {
		return p.lat, p.lon, true
	}
	if i := strings.IndexByte(s, ','); i > 0 {
		la, e1 := strconv.ParseFloat(strings.TrimSpace(s[:i]), 64)
		lo, e2 := strconv.ParseFloat(strings.TrimSpace(s[i+1:]), 64)
		if e1 == nil && e2 == nil && math.Abs(la) <= 90 && math.Abs(lo) <= 180 {
			return la, lo, true
		}
	}
	// region ids like "sgp1"/"fra1" variants or "eu-central"
	for _, cut := range []string{"-", "_"} {
		if i := strings.Index(s, cut); i > 0 {
			if p, ok := regionGeo[s[:i]]; ok {
				return p.lat, p.lon, true
			}
		}
	}
	if len(s) > 3 {
		if p, ok := regionGeo[strings.TrimRight(s, "0123456789")]; ok {
			return p.lat, p.lon, true
		}
	}
	return 0, 0, false
}

// EstimateLatencyMS estimates round-trip time between a location and a
// region: great-circle distance over fibre (~200 km/ms one way) × 1.5 path
// stretch, ×2 for the round trip, + 5 ms. Returns -1 when unknown. A
// "local" region (dev/self-hosted) is assumed to be next to core (5 ms).
func EstimateLatencyMS(from, region string) float64 {
	if strings.EqualFold(region, "local") {
		return 5
	}
	la1, lo1, ok1 := Coord(from)
	la2, lo2, ok2 := Coord(region)
	if !ok1 || !ok2 {
		return -1
	}
	d := haversineKM(la1, lo1, la2, lo2)
	return math.Round(d*1.5/200*2 + 5)
}

func haversineKM(la1, lo1, la2, lo2 float64) float64 {
	const r = 6371.0
	rad := math.Pi / 180
	dla, dlo := (la2-la1)*rad, (lo2-lo1)*rad
	a := math.Sin(dla/2)*math.Sin(dla/2) + math.Cos(la1*rad)*math.Cos(la2*rad)*math.Sin(dlo/2)*math.Sin(dlo/2)
	return 2 * r * math.Asin(math.Sqrt(a))
}
