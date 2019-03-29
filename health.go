package health

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"
)

// A Registry is a collection of checks. Most applications will use the global
// registry defined in DefaultRegistry. However, unit tests may need to create
// separate registries to isolate themselves from other tests.
type Registry struct {
	mu               sync.RWMutex
	registeredChecks map[string]Checker
}

// NewRegistry creates a new registry. This isn't necessary for normal use of
// the package, but may be useful for unit tests so individual tests have their
// own set of checks.
func NewRegistry() *Registry {
	return &Registry{
		registeredChecks: make(map[string]Checker),
	}
}

// DefaultRegistry is the default registry where checks are registered. It is
// the registry used by the HTTP handler.
var DefaultRegistry *Registry

type Result struct {
	Error   error
	Message string
}

// Checker is the interface for a Health Checker
type Checker interface {
	// Check returns nil if the service is okay.
	Check() Result
}

// CheckFunc is a convenience type to create functions that implement
// the Checker interface
type CheckFunc func() Result

// Check Implements the Checker interface to allow for any func() error method
// to be passed as a Checker
func (cf CheckFunc) Check() Result {
	return cf()
}

// Updater implements a health check that is explicitly set.
type Updater interface {
	Checker

	// Update updates the current status of the health check.
	Update(status Result)
}

// updater implements Checker and Updater, providing an asynchronous Update
// method.
// This allows us to have a Checker that returns the Check() call immediately
// not blocking on a potentially expensive check.
type updater struct {
	mu     sync.Mutex
	status Result
}

// Check implements the Checker interface
func (u *updater) Check() Result {
	u.mu.Lock()
	defer u.mu.Unlock()

	return u.status
}

// Update implements the Updater interface, allowing asynchronous access to
// the status of a Checker.
func (u *updater) Update(status Result) {
	u.mu.Lock()
	defer u.mu.Unlock()

	u.status = status
}

// NewStatusUpdater returns a new updater
func NewStatusUpdater() Updater {
	return &updater{}
}

// PeriodicChecker wraps an updater to provide a periodic checker
func PeriodicChecker(check Checker, period time.Duration) Checker {
	u := NewStatusUpdater()
	go func() {
		t := time.NewTicker(period)
		for {
			<-t.C
			u.Update(check.Check())
		}
	}()

	return u
}

type HealthCheck struct {
	Healthy bool   `json:"healthy"`
	Message string `json:"message"`
}

type Status map[string]HealthCheck

// CheckStatus returns a map with all the current health check errors
func (registry *Registry) CheckStatus() Status {
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	status := Status{}

	for k, v := range registry.registeredChecks {
		res := v.Check()

		healthy := res.Error == nil

		status[k] = HealthCheck{
			Healthy: healthy,
			Message: res.Message,
		}
	}

	return status
}

// CheckStatus returns a map with all the current health check results from the
// default registry.
func CheckStatus() Status {
	return DefaultRegistry.CheckStatus()
}

// Register associates the checker with the provided name.
func (registry *Registry) Register(name string, check Checker) {
	if registry == nil {
		registry = DefaultRegistry
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	_, ok := registry.registeredChecks[name]
	if ok {
		panic("Check already exists: " + name)
	}
	registry.registeredChecks[name] = check
}

// Register associates the checker with the provided name in the default
// registry.
func Register(name string, check Checker) {
	DefaultRegistry.Register(name, check)
}

// RegisterFunc allows the convenience of registering a checker directly from
// an arbitrary func() error.
func (registry *Registry) RegisterFunc(name string, check CheckFunc) {
	registry.Register(name, check)
}

// RegisterFunc allows the convenience of registering a checker in the default
// registry directly from an arbitrary func() error.
func RegisterFunc(name string, check CheckFunc) {
	DefaultRegistry.RegisterFunc(name, check)
}

// RegisterPeriodicFunc allows the convenience of registering a PeriodicChecker
// from an arbitrary func() error.
func (registry *Registry) RegisterPeriodicFunc(name string, period time.Duration, check CheckFunc) {
	registry.Register(name, PeriodicChecker(check, period))
}

// RegisterPeriodicFunc allows the convenience of registering a PeriodicChecker
// in the default registry from an arbitrary func() error.
func RegisterPeriodicFunc(name string, period time.Duration, check CheckFunc) {
	DefaultRegistry.RegisterPeriodicFunc(name, period, check)
}

// StatusHandler returns a JSON blob with all the currently registered Health Checks
// and their corresponding status.
// Returns 503 if any Error status exists, 200 otherwise
func StatusHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method == "GET" {
		checks := CheckStatus()
		isFailing := false
		for _, v := range checks {
			if !v.Healthy {
				isFailing = true
			}
		}

		status := http.StatusOK
		if isFailing {
			// If there is an error, return 503
			status = http.StatusServiceUnavailable
		}

		statusResponse(w, r, status, checks)
	} else {
		http.NotFound(w, r)
	}
}

// statusResponse completes the request with a response describing the health
// of the service.
func statusResponse(w http.ResponseWriter, r *http.Request, status int, checks Status) {
	p, err := json.Marshal(checks)
	if err != nil {
		log.Printf("error serializing health status: %v", err)
		p, err = json.Marshal(struct {
			ServerError string `json:"server_error"`
		}{
			ServerError: "Could not parse error message",
		})
		status = http.StatusInternalServerError

		if err != nil {
			log.Printf("error serializing health status failure message: %v", err)
			return
		}
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Length", fmt.Sprint(len(p)))
	w.WriteHeader(status)
	if _, err := w.Write(p); err != nil {
		log.Printf("error writing health status response body: %v", err)
	}
}

// Registers global /debug/health api endpoint, creates default registry
func init() {
	DefaultRegistry = NewRegistry()
}
