// Package runner executes every request in a collection (or one folder of
// it) in tree order, optionally once per row of an iteration data set —
// hapidays's answer to Postman's Collection Runner, which as of 2026 is
// rate-limited/paywalled on the free tier. This one has no tier.
package runner

import (
	"context"
	"time"

	"hapidays/internal/client"
	"hapidays/internal/model"
)

type Options struct {
	// Vars are the base (collection + environment) variables; each
	// iteration's data row is merged on top, taking precedence.
	Vars     map[string]string
	DataRows []map[string]string // one run through all requests per row; nil/empty = single run with just Vars
	DelayMS  int
	Client   client.Options
	// OnCapture, if set, is called after each request that captured values
	// (see model.Capture), so the caller can persist them — e.g. into the
	// selected environment, as a manual Send does. Independently of this,
	// captured values are merged into the run's own variables so later
	// requests see them, matching what stepping through by hand would do.
	OnCapture func(captured map[string]string)
}

// Run executes nodes (a collection's root, or one folder's children) and
// returns one RunStepResult per request per iteration, in execution order.
// It does not stop on failure — a 4xx/5xx or transport error is recorded
// and the run continues, matching how a smoke-test pass over many
// endpoints is normally used.
func Run(ctx context.Context, nodes []*model.Node, opts Options) []model.RunStepResult {
	requests := Flatten(nodes)

	iterations := 1
	if len(opts.DataRows) > 0 {
		iterations = len(opts.DataRows)
	}

	var results []model.RunStepResult
	// Captured values accumulate across requests and iterations; a data row
	// still takes precedence over them, as it does over the base vars.
	captured := map[string]string{}
	for i := 0; i < iterations; i++ {
		for _, node := range requests {
			select {
			case <-ctx.Done():
				return results
			default:
			}

			// Rebuilt per request so a value captured by an earlier request
			// (a CSRF token, say) is visible to this one.
			vars := mergeVars(mergeVars(opts.Vars, captured), rowAt(opts.DataRows, i))

			step := model.RunStepResult{
				Iteration: i,
				NodeID:    node.ID,
				Name:      node.Name,
				Method:    node.Request.Method,
				URL:       client.Resolve(node.Request.URLRaw, vars),
			}
			step.AssertionsPassed = true
			var result *client.Result
			var err error
			if node.Request.Body.Mode == model.BodyGRPC {
				result, err = client.ExecuteGRPC(ctx, *node.Request, vars, opts.Client)
			} else {
				result, err = client.Execute(ctx, *node.Request, vars, opts.Client)
			}
			switch {
			case err != nil:
				step.Error = err.Error()
			case result.Error != "":
				step.Error = result.Error
				step.DurationMS = result.DurationMS
			default:
				step.Status = result.Status
				step.DurationMS = result.DurationMS
				step.SizeBytes = result.SizeBytes
				step.Assertions = result.Assertions
				if len(result.Captured) > 0 {
					for k, v := range result.Captured {
						captured[k] = v
					}
					if opts.OnCapture != nil {
						opts.OnCapture(result.Captured)
					}
				}
				for _, a := range result.Assertions {
					if !a.Passed {
						step.AssertionsPassed = false
						break
					}
				}
			}
			results = append(results, step)

			if opts.DelayMS > 0 {
				time.Sleep(time.Duration(opts.DelayMS) * time.Millisecond)
			}
		}
	}
	return results
}

// Flatten walks nodes depth-first and returns every request leaf, in tree
// order — the same traversal Run uses, exported so other callers (the
// OData $batch handler) can build the same "every request under this
// folder" selection without a second copy of the walk.
func Flatten(nodes []*model.Node) []*model.Node {
	var out []*model.Node
	for _, n := range nodes {
		if n.Request != nil {
			out = append(out, n)
			continue
		}
		out = append(out, Flatten(n.Children)...)
	}
	return out
}

func rowAt(rows []map[string]string, i int) map[string]string {
	if i < len(rows) {
		return rows[i]
	}
	return nil
}

func mergeVars(base, row map[string]string) map[string]string {
	merged := make(map[string]string, len(base)+len(row))
	for k, v := range base {
		merged[k] = v
	}
	for k, v := range row {
		merged[k] = v
	}
	return merged
}

// FindNode locates a node (folder or request) by ID within a tree, for
// scoping a run to one folder. Returns nil if not found.
func FindNode(nodes []*model.Node, id string) *model.Node {
	for _, n := range nodes {
		if n.ID == id {
			return n
		}
		if n.Children != nil {
			if found := FindNode(n.Children, id); found != nil {
				return found
			}
		}
	}
	return nil
}
