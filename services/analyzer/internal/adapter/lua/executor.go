// Package lua provides a gopher-lua based implementation of the LuaExecutor port.
package lua

import (
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	lua "github.com/yuin/gopher-lua"

	"github.com/mclyashko/anomaly-detector/services/analyzer/internal/core"
)

const (
	// executionTimeout is the max duration for a single Lua script execution.
	executionTimeout = 5 * time.Second
)

// execResult holds the result of a Lua script execution.
type execResult struct {
	triggered bool
	message   string
	err       error
}

// GopherLua implements port.LuaExecutor using gopher-lua.
// Script source is cached per path and re-parsed on each execution.
// Each execution creates a fresh LState with sandboxed libraries.
type GopherLua struct {
	logger *slog.Logger
	mu     sync.Mutex
	// cache maps absolute script path → source content
	cache map[string]string
}

// New creates a new GopherLua executor.
func New(logger *slog.Logger) *GopherLua {
	return &GopherLua{
		logger: logger,
		cache:  make(map[string]string),
	}
}

// Execute runs the Lua script at scriptPath with the given metric.
// It loads the script from disk, compiles it with sandboxed libraries,
// and calls the user's evaluate(metric) function.
// Returns (triggered, message, error).
func (g *GopherLua) Execute(scriptPath string, metric core.Metric) (bool, string, error) {
	// Load and cache script source.
	script, err := g.loadScript(scriptPath)
	if err != nil {
		return false, "", err
	}

	// Execute in a goroutine so we can timeout.
	done := make(chan execResult, 1)

	go func() {
		res := g.execute(script, metric, scriptPath)
		done <- res
	}()

	select {
	case res := <-done:
		return res.triggered, res.message, res.err
	case <-time.After(executionTimeout):
		return false, "", errors.New("lua script timed out after " + executionTimeout.String())
	}
}

// loadScript reads the script from disk and caches the content.
func (g *GopherLua) loadScript(scriptPath string) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	if src, ok := g.cache[scriptPath]; ok {
		return src, nil
	}

	// Resolve absolute path to use as cache key.
	absPath, err := resolvePath(scriptPath)
	if err != nil {
		return "", err
	}

	src, err := os.ReadFile(absPath)
	if err != nil {
		return "", err
	}

	g.cache[absPath] = string(src)
	return string(src), nil
}

// execute compiles and runs the script with the given metric.
func (g *GopherLua) execute(script string, metric core.Metric, scriptPath string) execResult {
	// Create a fresh LState with sandboxed libraries.
	L, err := g.newState()
	if err != nil {
		return execResult{err: err}
	}
	defer L.Close()

	// Build a metric table that the script can access.
	metricTable := L.NewTable()
	L.SetField(metricTable, "name", lua.LString(metric.Name))
	L.SetField(metricTable, "value", lua.LNumber(metric.Value))
	L.SetField(metricTable, "timestamp", lua.LString(metric.Timestamp.Format(time.RFC3339Nano)))
	L.SetGlobal("metric", metricTable) // script can use 'metric' as a global

	// Protect execution with panic recovery.
	var triggered bool
	var msg string
	var runErr error

	func() {
		defer func() {
			if r := recover(); r != nil {
				runErr = errors.New("lua panic")
				g.logger.Warn("lua script panicked",
					"script", scriptPath,
					"panic", r,
				)
			}
		}()

		// Combine the user's script with a call to evaluate(metric).
		// Since 'metric' is set as a global above, the script can access it.
		combined := script + "\nreturn evaluate(metric)"
		if err := L.DoString(combined); err != nil {
			runErr = err
			return
		}

		// Get return values from the stack.
		// Stack: [bool triggered, string msg]
		if L.GetTop() >= 1 {
			triggered = L.ToBool(1)
		}
		if L.GetTop() >= 2 {
			msg = L.ToString(2)
		}
	}()

	return execResult{triggered: triggered, message: msg, err: runErr}
}

// newState creates a fresh LState with safe libraries only.
func (g *GopherLua) newState() (*lua.LState, error) {
	L := lua.NewState(lua.Options{
		SkipOpenLibs: false,
	})

	// Remove dangerous globals immediately after state creation.
	// This gives us access to base library functions (tostring, type, etc.)
	// while removing things that could escape the sandbox.
	dangerousGlobals := []string{
		"os", "io", "debug", "loadfile", "loadstring",
		"require", "dofile", "package",
		"rawget", "rawset", "setmetatable", "getmetatable",
		"collectgarbage",
	}
	for _, name := range dangerousGlobals {
		L.SetGlobal(name, lua.LNil)
	}

	return L, nil
}

// resolvePath returns the absolute path for a potentially relative path.
func resolvePath(p string) (string, error) {
	if filepath.IsAbs(p) {
		return p, nil
	}
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	return filepath.Join(wd, p), nil
}

// Close is a no-op for this implementation.
func (g *GopherLua) Close() error {
	return nil
}

// Verify GopherLua satisfies core.LuaExecutor.
var _ interface {
	Execute(string, core.Metric) (bool, string, error)
} = (*GopherLua)(nil)
