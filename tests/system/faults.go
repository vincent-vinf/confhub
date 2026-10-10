package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/gorilla/websocket"
	sdk "github.com/vincent-vinf/confhub/sdk/go"
)

func (r *runner) fault(action string, index int) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "POST", fmt.Sprintf("%s/%s?index=%d", r.control, action, index), bytes.NewReader(nil))
	must(err)
	req.Header.Set("Authorization", "Bearer "+r.controlToken)
	res, err := http.DefaultClient.Do(req)
	must(err)
	defer res.Body.Close()
	require(res.StatusCode == 200, "isolated controller %s failed HTTP %d", action, res.StatusCode)
}
func (r *runner) faults() {
	firstResult := len(r.report.Results)
	defer func() {
		for _, res := range r.report.Results[firstResult:] {
			require(res.Error == "", "fault subscenario failed: %s", res.Name)
		}
	}()
	r.scene("faults/slow-connection", r.slowConnection)
	k := r.key("faults")
	s := r.save(0, k, state{}, "before-fault", "").State
	dir, err := os.MkdirTemp("", "confhub-system-cache-")
	must(err)
	defer os.RemoveAll(dir)
	addresses := []string{r.addresses[2], r.addresses[0], r.addresses[1]}
	c := r.sdk(addresses, map[string]string{"run": r.namespace, "sys.hostname": "fault-sdk"}, dir)
	defer closeSDK(c)
	o := newObserver()
	must(c.Subscribe(r.ctx, k, o.callback))
	r.observed(o, k, s, "before-fault", "", 1)
	p := r.pythonProbe(map[string]string{"run": r.namespace}, "faults")
	defer p.close()
	p.rpc(object{"op": "subscribe", "key": k})
	r.pythonObserved(p, k, s, "before-fault", "", 1)
	r.scene("faults/instance-failover", func() {
		r.wait("fault connection registered", func() bool { return len(r.online(0)) >= 2 })
		var oldInstance string
		for _, row := range r.online(0) {
			if row.Tags["sys.hostname"] == "fault-sdk" {
				oldInstance = row.Instance
			}
		}
		require(oldInstance != "", "fault SDK connection absent")
		r.fault("stop", 2)
		defer r.fault("start", 2)
		s = r.save(0, k, s, "after-node-exit", "").State
		r.observed(o, k, s, "after-node-exit", "", 2)
		r.pythonObserved(p, k, s, "after-node-exit", "", 2)
		r.wait("expired instance lease", func() bool {
			for _, row := range r.online(0) {
				if row.Instance == oldInstance {
					return false
				}
			}
			return true
		})
	})
	r.scene("faults/database-outage", func() {
		r.fault("database-down", 0)
		defer r.fault("database-up", 0)
		r.wait("all replicas unready", func() bool {
			for i := range r.addresses {
				code, _, err := r.request(i, "GET", "/health/ready", nil)
				if err != nil || code != 503 {
					return false
				}
			}
			return true
		})
		value, err := c.Get(r.ctx, k)
		must(err)
		require(value.Content == "after-node-exit" && value.Source == sdk.Memory, "outage lost last successful value")
		empty := r.sdk(r.addresses, nil, "")
		func() {
			defer closeSDK(empty)
			_, err := empty.Get(r.ctx, k)
			require(errors.Is(err, sdk.ErrUnavailable), "uncached offline GET did not fail")
		}()
		var py struct {
			Value  sdk.Snapshot `json:"value"`
			Source string       `json:"source"`
		}
		decode(p.rpc(object{"op": "get", "key": k}), &py)
		require(py.Source == "memory" && py.Value.Content == "after-node-exit", "Python outage cache wrong")
		r.fault("database-up", 0)
		r.wait("all replicas recover", func() bool {
			for i := range r.addresses {
				code, _, err := r.request(i, "GET", "/health/ready", nil)
				if err != nil || code != 200 {
					return false
				}
			}
			return true
		})
		s = r.save(0, k, s, "after-database", "").State
		r.observed(o, k, s, "after-database", "", 3)
		r.pythonObserved(p, k, s, "after-database", "", 3)
	})
	r.scene("faults/disconnected-publications-and-cache", func() {
		// Isolate one actual running replica while other nodes publish and clean logs.
		pinned := r.sdk([]string{r.addresses[1]}, nil, dir+"-pinned")
		defer os.RemoveAll(dir + "-pinned")
		defer closeSDK(pinned)
		po := newObserver()
		must(pinned.Subscribe(r.ctx, k, po.callback))
		r.observed(po, k, s, "after-database", "", 3)
		r.fault("isolate", 1)
		defer r.fault("heal", 1)
		r.wait("isolated replica unready", func() bool {
			code, _, err := r.request(1, "GET", "/health/ready", nil)
			return err == nil && code == 503
		})
		for i := 0; i < 4; i++ {
			s = r.save(0, k, s, fmt.Sprintf("offline-publication-%d", i), "").State
		}
		r.observed(o, k, s, "offline-publication-3", "", 7)
		// The isolated environment configures two-second event retention. The
		// controller acknowledges only after the retention window has elapsed.
		r.fault("wait-retention", 1)
		r.fault("heal", 1)
		r.wait("isolated replica recovered", func() bool {
			code, _, err := r.request(1, "GET", "/health/ready", nil)
			return err == nil && code == 200
		})
		r.observed(po, k, s, "offline-publication-3", "", 7)
		// A disk restart with the same cluster/tag context retains last good content.
		closeSDK(pinned)
		r.fault("isolate", 1)
		r.wait("replica unready before disk restart", func() bool {
			code, _, err := r.request(1, "GET", "/health/ready", nil)
			return err == nil && code == 503
		})
		restarted := r.sdk([]string{r.addresses[1]}, nil, dir+"-pinned")
		func() {
			defer closeSDK(restarted)
			value, err := restarted.Get(r.ctx, k)
			must(err)
			require(value.Source == sdk.Disk && value.Content == "offline-publication-3", "disk restart lost authoritative value")
		}()
		r.fault("heal", 1)
	})
	r.scene("faults/heartbeat-timeout", func() {
		endpoint := "ws" + r.addresses[0][4:] + "/api/client/watch"
		conn, _, err := websocket.DefaultDialer.Dial(endpoint, nil)
		must(err)
		defer conn.Close()
		conn.SetPingHandler(func(string) error { return nil })
		conn.SetReadDeadline(time.Now().Add(35 * time.Second))
		start := time.Now()
		for {
			_, _, err = conn.ReadMessage()
			if err != nil {
				break
			}
		}
		require(time.Since(start) >= 25*time.Second, "connection closed before heartbeat deadline")
		var netErr interface{ Timeout() bool }
		require(!(errors.As(err, &netErr) && netErr.Timeout()), "server did not close missing-pong connection")
	})
	// Deletion must persist into an offline disk restart, not resurrect old content.
	r.scene("faults/deletion-cache", func() {
		r.status(0, "DELETE", configPath(k), baseline(s), 200)
		r.wait("delete received before offline restart", func() bool { value, _ := o.value(k); return value.Deleted })
		closeSDK(c)
		r.fault("database-down", 0)
		defer r.fault("database-up", 0)
		r.wait("replicas unready before deleted-cache restart", func() bool {
			for i := range r.addresses {
				code, _, err := r.request(i, "GET", "/health/ready", nil)
				if err != nil || code != 503 {
					return false
				}
			}
			return true
		})
		restarted := r.sdk(addresses, map[string]string{"run": r.namespace, "sys.hostname": "fault-sdk"}, dir)
		defer closeSDK(restarted)
		value, err := restarted.Get(r.ctx, k)
		require(errors.Is(err, sdk.ErrNotFound) || errors.Is(err, sdk.ErrUnavailable), "deleted cache resurrected: %v", err)
		require(value.Content == "", "deleted cache exposed old content")
	})
}
