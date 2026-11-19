package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/A2MaTex/a2matex-runtime/internal/validation/editorgraph"
	"github.com/A2MaTex/a2matex-runtime/internal/validation/nodecontract"
	validationservice "github.com/A2MaTex/a2matex-runtime/internal/validation/service"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	"github.com/google/uuid"
)

func TestVariableCompileStartPollAndReplay(t *testing.T) {
	f := startStore(t)
	close(f.resume)
	raw, err := os.ReadFile("../../examples/g18/compile-score.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []string{"low", "high", "replace", "failed overwrite", "fresh empty", "fresh normal"} {
		t.Run(scenario, func(t *testing.T) {
			var example struct {
				EditorGraph editorgraph.Graph `json:"editorGraph"`
				InputSchema json.RawMessage   `json:"inputSchema"`
			}
			if err := json.Unmarshal(raw, &example); err != nil {
				t.Fatal(err)
			}
			for i := range example.EditorGraph.Nodes {
				if example.EditorGraph.Nodes[i].Type == "flow.delay" {
					example.EditorGraph.Nodes[i].Bindings["durationMs"] = []byte(`{"kind":"LITERAL","value":0}`)
				}
			}
			branch, attempts, status := "low_wait", 6, constants.ExecutionSucceeded
			inputs := `{"score":30}`
			switch scenario {
			case "high":
				inputs, branch = `{"score":75}`, "high_wait"
			case "replace", "failed overwrite":
				extra := editorgraph.Node{ID: "try_replace", Type: constants.SetVariableNodeType,
					Config:   map[string]json.RawMessage{"scope": []byte(`"FLOW"`), "name": []byte(`"score"`)},
					Bindings: map[string]json.RawMessage{"value": []byte(`{"kind":"LITERAL","value":75}`)}, ErrorPolicy: constants.ErrorPolicyStop}
				if scenario == "failed overwrite" {
					extra.Bindings["value"] = []byte(`{"kind":"FLOW_VARIABLE","name":"missing"}`)
					extra.ErrorPolicy = constants.ErrorPolicyErrorBranch
					example.EditorGraph.Edges = append(example.EditorGraph.Edges, editorgraph.Edge{ID: "extra_error", SourceNodeID: extra.ID, SourcePort: constants.NodeErrorPort, TargetNodeID: "choose"})
				} else {
					branch = "high_wait"
				}
				example.EditorGraph.Nodes = append(example.EditorGraph.Nodes, extra)
				for i := range example.EditorGraph.Edges {
					if example.EditorGraph.Edges[i].SourceNodeID == "set_threshold" {
						example.EditorGraph.Edges[i].TargetNodeID = extra.ID
					}
				}
				example.EditorGraph.Edges = append(example.EditorGraph.Edges, editorgraph.Edge{ID: "extra_success", SourceNodeID: extra.ID, SourcePort: constants.NodeSuccessPort, TargetNodeID: "choose"})
				attempts++
			case "fresh empty":
				example.EditorGraph.Nodes = append(example.EditorGraph.Nodes[:1:1], example.EditorGraph.Nodes[3:]...)
				edges := []editorgraph.Edge{{ID: "start_choose", SourceNodeID: "start", SourcePort: constants.NodeSuccessPort, TargetNodeID: "choose"}}
				for _, edge := range example.EditorGraph.Edges {
					if edge.SourceNodeID == "choose" || edge.TargetNodeID == "end" {
						edges = append(edges, edge)
					}
				}
				example.EditorGraph.Edges = edges
				branch, attempts, status = "", 2, constants.ExecutionFailed
			}
			body, err := json.Marshal(example)
			if err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest(http.MethodPost, "/api/v1/workflow-compilation", bytes.NewReader(body))
			req.Header.Set("Authorization", "Bearer "+f.token)
			req.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			f.router.ServeHTTP(response, req)
			var compiled validationservice.CompilationResult
			if err := json.Unmarshal(response.Body.Bytes(), &compiled); err != nil {
				t.Fatal(err)
			}
			if response.Code != http.StatusOK || compiled.Validation.Status != "VALID" || len(compiled.RuntimeIR) == 0 {
				t.Fatalf("compile: %d %s", response.Code, response.Body.String())
			}
			f.request.RuntimeIR, f.request.RequestID = compiled.RuntimeIR, uuid.NewString()
			f.request.Devices[0].Inputs = []byte(inputs)
			created := f.post(t, t.Context(), f.request, http.StatusCreated)
			terminal := f.settled(t, created.RunGroup.ID, status)
			page, err := f.repo.Attempts(t.Context(), terminal.DeviceExecutions[0].ID, 0, constants.MaxAttemptPageSize)
			if err != nil || len(page.Attempts) != attempts {
				t.Fatal(page, err)
			}
			seenBranch, seenFailure := false, false
			for _, attempt := range page.Attempts {
				if attempt.NodeID == branch {
					seenBranch = true
				}
				if attempt.Status == constants.ExecutionFailed {
					seenFailure = true
					if !strings.Contains(string(attempt.Error), constants.CommandInputUnsupported) || attempt.Output != nil {
						t.Fatal(attempt)
					}
				} else if attempt.Status != constants.ExecutionSucceeded {
					t.Fatal(attempt)
				}
				if strings.HasPrefix(attempt.NodeID, "set_") || attempt.NodeID == "choose" || attempt.NodeID == "try_replace" {
					if attempt.Status == constants.ExecutionSucceeded && string(attempt.Output) != "{}" {
						t.Fatal("assignment or route leaked into output")
					}
				}
			}
			if (branch != "" && !seenBranch) || seenFailure != (scenario == "failed overwrite" || scenario == "fresh empty") {
				t.Fatal(page)
			}
			poll := httptest.NewRequest(http.MethodGet, "/api/v1/runs/"+created.RunGroup.ID, nil)
			poll.Header.Set("Authorization", "Bearer "+f.token)
			polled := httptest.NewRecorder()
			f.router.ServeHTTP(polled, poll)
			if polled.Code != http.StatusOK || !strings.Contains(polled.Body.String(), `"status":"`+status+`"`) {
				t.Fatal(polled.Code, polled.Body.String())
			}
			before := f.counts(t)
			if replay := f.post(t, t.Context(), f.request, http.StatusOK); replay.RunGroup.ID != created.RunGroup.ID || f.counts(t) != before {
				t.Fatal("replay changed execution")
			}
		})
	}
}

func TestVariableRunIntentionalStopRetainsCommittedAssignments(t *testing.T) {
	f := startStore(t)
	raw, err := os.ReadFile("../../examples/g18/compile-score.json")
	if err != nil {
		t.Fatal(err)
	}
	var example map[string]json.RawMessage
	if err := json.Unmarshal(raw, &example); err != nil {
		t.Fatal(err)
	}
	catalog, err := nodecontract.NewDefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	compiled, err := validationservice.New(catalog).Compile(example["editorGraph"], example["inputSchema"])
	if err != nil || compiled.Validation.Status != "VALID" {
		t.Fatal(compiled, err)
	}
	f.request.RuntimeIR, f.request.RequestID = compiled.RuntimeIR, uuid.NewString()
	f.request.Devices[0].Inputs = []byte(`{"score":30}`)
	created := f.post(t, t.Context(), f.request, http.StatusCreated)
	f.waitDelay(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	deadline, _ := ctx.Deadline()
	f.lifetime.PrepareStop(constants.ExecutionStopIntentional, deadline)
	if err := f.lifetime.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	terminal, err := f.repo.SnapshotByID(t.Context(), created.RunGroup.ID)
	if err != nil || terminal.RunGroup.Status != constants.ExecutionCancelled {
		t.Fatal(terminal, err)
	}
	page, err := f.repo.Attempts(t.Context(), terminal.DeviceExecutions[0].ID, 0, constants.MaxAttemptPageSize)
	if err != nil || len(page.Attempts) != 5 {
		t.Fatal(page, err)
	}
	for i, row := range page.Attempts {
		if i < 4 && row.Status != constants.ExecutionSucceeded {
			t.Fatal(row)
		}
		if i == 4 && (row.NodeID != "low_wait" || row.Status != constants.ExecutionCancelled) {
			t.Fatal(row)
		}
		if (row.NodeID == "set_score" || row.NodeID == "set_threshold") && string(row.Output) != "{}" {
			t.Fatal(row)
		}
	}
}
