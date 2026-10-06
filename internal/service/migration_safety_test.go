package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/SakuraOpenSource/virtualis/internal/agentclient"
	"github.com/SakuraOpenSource/virtualis/internal/model"
	"gorm.io/gorm"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMigrationFailuresNeverDeleteSourceOrUnownedTargetAndKeepRecovery(t *testing.T) {
	for _, failure := range []string{"import-existing", "verify", "switch", "source-delete"} {
		t.Run(failure, func(t *testing.T) {
			var sourceDeletes, targetDeletes int
			f := newLifecycleFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "DELETE" {
					sourceDeletes++
					if failure == "source-delete" {
						http.Error(w, "source delete failed", 502)
					} else {
						w.WriteHeader(204)
					}
					return
				}
				if strings.HasSuffix(r.URL.Path, "/export") {
					fmt.Fprint(w, "archive")
					return
				}
				replyLifecycle(w, decodeLifecycleRequest(t, r))
			})
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/drivers" {
					fmt.Fprint(w, `{"items":[{"name":"qemu","available":true}]}`)
					return
				}
				if r.URL.Path == "/api/host/network" {
					fmt.Fprint(w, `{"network":{"ipv4_count":2}}`)
					return
				}
				if r.Method == "DELETE" {
					targetDeletes++
					w.WriteHeader(204)
					return
				}
				if strings.HasSuffix(r.URL.Path, "/import") {
					if failure == "import-existing" {
						http.Error(w, "target already exists", 502)
						return
					}
					if err := r.ParseMultipartForm(1 << 20); err != nil {
						t.Error(err)
					}
					defer r.MultipartForm.RemoveAll()
					var inst agentclient.Instance
					json.Unmarshal([]byte(r.FormValue("instance")), &inst)
					replyLifecycle(w, inst)
					return
				}
				inst := decodeLifecycleRequest(t, r)
				if failure == "verify" {
					inst.Status = "running"
				}
				replyLifecycle(w, inst)
			}))
			defer target.Close()
			agent := model.Agent{Name: "target", Status: "online", Endpoint: target.URL, Token: "test-token", TokenHash: "hash", Arch: "amd64"}
			if err := f.db.Create(&agent).Error; err != nil {
				t.Fatal(err)
			}
			oldPool := model.IPPoolEntry{AgentID: f.agent.ID, IP: "192.0.2.1", Status: "assigned", InstanceID: &f.inst.ID}
			newPool := model.IPPoolEntry{AgentID: agent.ID, IP: "192.0.2.2", Status: "free"}
			f.db.Create(&oldPool)
			f.db.Create(&newPool)
			if failure == "switch" {
				f.db.Callback().Update().Before("gorm:update").Register("fail_switch", func(tx *gorm.DB) {
					if tx.Statement.Table == "instances" {
						if values, ok := tx.Statement.Dest.(map[string]any); ok && values["agent_id"] != nil {
							tx.AddError(errors.New("database switch injected failure"))
						}
					}
				})
				defer f.db.Callback().Update().Remove("fail_switch")
			}
			_, err := f.svc.MigrateInstance(context.Background(), f.inst.ID, MigrationInput{TargetAgentID: agent.ID, IPPoolEntryID: &newPool.ID, Network: &model.NetworkConfig{Mode: "dedicated"}})
			if err == nil {
				t.Fatal("failure returned success")
			}
			inst, _ := f.svc.GetInstance(f.inst.ID)
			var m model.Migration
			f.db.Last(&m)
			if failure == "verify" {
				if targetDeletes != 1 || sourceDeletes != 0 || inst.BusyOperation != "" {
					t.Fatalf("verify cleanup source=%d target=%d inst=%+v", sourceDeletes, targetDeletes, inst)
				}
				f.db.First(&newPool, newPool.ID)
				if newPool.InstanceID != nil {
					t.Fatal("target pool not released after owned cleanup")
				}
			} else {
				if targetDeletes != 0 || inst.BusyOperation == "" || inst.RecoveryError == "" {
					t.Fatalf("unsafe uncertain failure target=%d inst=%+v", targetDeletes, inst)
				}
				file, e := f.svc.storage.Open(m.ArchivePath)
				if e != nil {
					t.Fatal("recovery archive lost", e)
				}
				file.Close()
				f.db.First(&oldPool, oldPool.ID)
				f.db.First(&newPool, newPool.ID)
				if oldPool.InstanceID == nil || newPool.InstanceID == nil {
					t.Fatal("recoverable ownership prematurely released")
				}
				if _, e := NewVirtualisService(f.db).PowerInstance(context.Background(), f.inst.ID, "start"); e == nil {
					t.Fatal("dual start allowed")
				}
			}
			if failure != "source-delete" && sourceDeletes != 0 {
				t.Fatal("source deleted on early failure")
			}
			if failure == "source-delete" && (*inst.AgentID != agent.ID || sourceDeletes != 1) {
				t.Fatal("source-delete failure lost switched ownership")
			}
		})
	}
}

func TestMigrationRejectsDestinationArchitectureAndDriverBeforeExport(t *testing.T) {
	f := newLifecycleFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/export") {
			t.Error("export before target validation")
		}
		replyLifecycle(w, decodeLifecycleRequest(t, r))
	})
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"items":[{"name":"qemu","available":false}]}`)
	}))
	defer remote.Close()
	agent := model.Agent{Name: "wrong", Status: "online", Endpoint: remote.URL, Token: "test-token", TokenHash: "hash", Arch: "arm64"}
	f.db.Create(&agent)
	if _, err := f.svc.MigrateInstance(context.Background(), f.inst.ID, MigrationInput{TargetAgentID: agent.ID}); err == nil {
		t.Fatal("wrong arch accepted")
	}
	f.db.Model(&agent).Update("arch", "amd64")
	if _, err := f.svc.MigrateInstance(context.Background(), f.inst.ID, MigrationInput{TargetAgentID: agent.ID}); err == nil {
		t.Fatal("unavailable driver accepted")
	}
}
