package adversarial

import "context"

// Engine implements the server.AdversarialEngine contract.
//
// The destructive scenarios in this package are currently expressed as Go
// tests. The admin chaos endpoint can be wired to this engine without breaking
// the server build, while a future runner can execute selected scenarios here
// with explicit DB/runtime controls.
type Engine struct{}

func NewEngine() *Engine {
	return &Engine{}
}

func (e *Engine) RunFullSuite(ctx context.Context) (scenarios, failures int, logOutput string, err error) {
	if err := ctx.Err(); err != nil {
		return 0, 0, "", err
	}
	return 0, 0, "adversarial engine runner not configured; run go test ./internal/adversarial for scenario execution", nil
}
