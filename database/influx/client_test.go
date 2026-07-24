package influxstore

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	influxdb2 "github.com/influxdata/influxdb-client-go/v2"
	"github.com/saramand9/kpro/database/common/lifecycle"
)

type influxServerLog struct {
	mu      sync.Mutex
	health  int
	writes  []influxWriteRequest
	queries []influxQueryRequest
}

type influxWriteRequest struct {
	Org    string
	Bucket string
	Body   string
}

type influxQueryRequest struct {
	Org  string
	Body string
}

func TestOpenChecksHealth(t *testing.T) {
	server, log := newInfluxTestServer(t, sampleQueryCSV())

	client, err := Open(context.Background(), Config{
		URL:    server.URL,
		Token:  "token",
		Org:    "team",
		Bucket: "metrics",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	if client.Name() != "influx" || client.Kind() != lifecycle.KindMetrics {
		t.Fatalf("resource=%s/%s", client.Name(), client.Kind())
	}
	if client.Org() != "team" || client.Bucket() != "metrics" {
		t.Fatalf("defaults=%q/%q", client.Org(), client.Bucket())
	}
	if health := client.Health(context.Background()); health.Status != lifecycle.StatusOK {
		t.Fatalf("health=%#v", health)
	}
	log.mu.Lock()
	defer log.mu.Unlock()
	if log.health != 2 {
		t.Fatalf("health calls=%d, want 2", log.health)
	}
}

func TestOpenValidatesConfig(t *testing.T) {
	if _, err := Open(context.Background(), Config{SkipHealthCheck: true}); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("missing URL err=%v, want ErrInvalidConfig", err)
	}

	server, _ := newInfluxTestServer(t, sampleQueryCSV())
	official := influxdb2.NewClient(server.URL, "token")
	defer official.Close()
	_, err := Open(context.Background(), Config{
		URL:             server.URL,
		Client:          official,
		SkipHealthCheck: true,
	})
	if !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("mixed Client and URL err=%v, want ErrInvalidConfig", err)
	}
}

func TestWriterAndMeasurementWritePointsAndRecords(t *testing.T) {
	server, log := newInfluxTestServer(t, sampleQueryCSV())
	client, err := Open(context.Background(), Config{
		URL:             server.URL,
		Token:           "token",
		Org:             "team",
		Bucket:          "metrics",
		SkipHealthCheck: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	err = client.Writer().WritePoint(context.Background(), NewPoint(
		"cpu",
		map[string]any{"usage": 0.42},
		WithTags(map[string]string{"host": "api"}),
		WithTime(time.Unix(1700000000, 123)),
	))
	if err != nil {
		t.Fatal(err)
	}
	err = client.Writer("custom").WriteRecord(context.Background(), "mem,host=api usage=10")
	if err != nil {
		t.Fatal(err)
	}
	err = client.Measurement("latency").WritePoint(context.Background(), Point{
		Tags:   map[string]string{"route": "/v1"},
		Fields: map[string]any{"p95": 12.5},
	})
	if err != nil {
		t.Fatal(err)
	}

	log.mu.Lock()
	defer log.mu.Unlock()
	if len(log.writes) != 3 {
		t.Fatalf("writes=%d, want 3", len(log.writes))
	}
	if log.writes[0].Org != "team" || log.writes[0].Bucket != "metrics" {
		t.Fatalf("first write target=%q/%q", log.writes[0].Org, log.writes[0].Bucket)
	}
	for _, want := range []string{"cpu", "host=api", "usage=0.42"} {
		if !strings.Contains(log.writes[0].Body, want) {
			t.Fatalf("point body %q missing %q", log.writes[0].Body, want)
		}
	}
	if log.writes[1].Bucket != "custom" || !strings.Contains(log.writes[1].Body, "mem,host=api usage=10") {
		t.Fatalf("record write=%#v", log.writes[1])
	}
	if !strings.Contains(log.writes[2].Body, "latency") || !strings.Contains(log.writes[2].Body, "p95=12.5") {
		t.Fatalf("measurement write=%#v", log.writes[2])
	}
}

func TestWriterValidatesPointAndTarget(t *testing.T) {
	if err := NewWriter(nil, "team", "metrics").WriteRecord(context.Background(), "x v=1"); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("nil writer err=%v, want ErrInvalidConfig", err)
	}
	client := &Client{}
	if err := NewWriter(client, "", "metrics").WriteRecord(context.Background(), "x v=1"); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("missing org err=%v, want ErrInvalidConfig", err)
	}
	if err := NewWriter(client, "team", "metrics").WritePoint(context.Background(), Point{}); !errors.Is(err, ErrInvalidPoint) {
		t.Fatalf("invalid point err=%v, want ErrInvalidPoint", err)
	}
}

func TestQueryRawAndParsed(t *testing.T) {
	server, log := newInfluxTestServer(t, sampleQueryCSV())
	client, err := Open(context.Background(), Config{
		URL:             server.URL,
		Token:           "token",
		Org:             "team",
		Bucket:          "metrics",
		SkipHealthCheck: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	query := client.Query()
	raw, err := query.QueryRaw(context.Background(), `from(bucket:"metrics")`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(raw, "cpu") {
		t.Fatalf("raw query result=%q", raw)
	}
	records, err := query.Query(context.Background(), `from(bucket:"metrics")`)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("records=%d, want 1", len(records))
	}
	if records[0].Values["_measurement"] != "cpu" || records[0].Values["host"] != "api" {
		t.Fatalf("record values=%#v", records[0].Values)
	}
	if value, ok := records[0].Values["_value"].(float64); !ok || value != 0.42 {
		t.Fatalf("record _value=%#v", records[0].Values["_value"])
	}

	log.mu.Lock()
	defer log.mu.Unlock()
	if len(log.queries) != 2 {
		t.Fatalf("queries=%d, want 2", len(log.queries))
	}
}

func TestQueryValidatesOrg(t *testing.T) {
	client := &Client{}
	_, err := NewQuery(client, "").QueryRaw(context.Background(), "from(bucket:\"x\")")
	if !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("missing org err=%v, want ErrInvalidConfig", err)
	}
}

func newInfluxTestServer(t *testing.T, queryCSV string) (*httptest.Server, *influxServerLog) {
	t.Helper()
	log := &influxServerLog{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health":
			log.mu.Lock()
			log.health++
			log.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"name":"influxdb","status":"pass"}`)
		case "/api/v2/write":
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatalf("read write body: %v", err)
			}
			log.mu.Lock()
			log.writes = append(log.writes, influxWriteRequest{
				Org:    r.URL.Query().Get("org"),
				Bucket: r.URL.Query().Get("bucket"),
				Body:   string(body),
			})
			log.mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		case "/api/v2/query":
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatalf("read query body: %v", err)
			}
			log.mu.Lock()
			log.queries = append(log.queries, influxQueryRequest{
				Org:  r.URL.Query().Get("org"),
				Body: string(body),
			})
			log.mu.Unlock()
			w.Header().Set("Content-Type", "text/csv")
			_, _ = io.WriteString(w, queryCSV)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server, log
}

func sampleQueryCSV() string {
	return `#datatype,string,long,dateTime:RFC3339,dateTime:RFC3339,dateTime:RFC3339,string,string,string,double
#group,false,false,true,true,false,true,true,true,false
#default,_result,,,,,,,,
,result,table,_start,_stop,_time,_measurement,_field,host,_value
,,0,2024-01-01T00:00:00Z,2024-01-01T01:00:00Z,2024-01-01T00:30:00Z,cpu,usage,api,0.42
`
}
