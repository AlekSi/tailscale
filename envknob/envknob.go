// Copyright (c) 2022 Tailscale Inc & AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package envknob provides access to environment-variable tweakable
// debug settings.
//
// These are primarily knobs used by Tailscale developers during
// development or by users when instructed to by Tailscale developers
// when debugging something. They are not a stable interface and may
// be removed or any time.
//
// A related package, control/controlknobs, are knobs that can be
// changed at runtime by the control plane. Sometimes both are used:
// an envknob for the default/explicit value, else falling back
// to the controlknob value.
package envknob

import (
	"log"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"tailscale.com/types/opt"
)

var (
	mu      sync.Mutex
	set     = map[string]string{}
	regStr  = map[string]*string{}
	regBool = map[string]*bool{}
)

func noteEnv(k, v string) {
	mu.Lock()
	defer mu.Unlock()
	noteEnvLocked(k, v)
}

func noteEnvLocked(k, v string) {
	if v != "" {
		set[k] = v
	} else {
		delete(set, k)
	}
}

// logf is logger.Logf, but logger depends on envknob, so for circular
// dependency reasons, make a type alias (so it's still assignable,
// but has nice docs here).
type logf = func(format string, args ...any)

// LogCurrent logs the currently set environment knobs.
func LogCurrent(logf logf) {
	mu.Lock()
	defer mu.Unlock()

	list := make([]string, 0, len(set))
	for k := range set {
		list = append(list, k)
	}
	sort.Strings(list)
	for _, k := range list {
		logf("envknob: %s=%q", k, set[k])
	}
}

// String returns the named environment variable, using os.Getenv.
//
// If the variable is non-empty, it's also tracked & logged as being
// an in-use knob.
func String(envVar string) string {
	v := os.Getenv(envVar)
	noteEnv(envVar, v)
	return v
}

// RegisterString returns a pointer to the value of the named environment
// variable. If envknob.Setenv is called, the pointed-to-value will be
// updated.
func RegisterString(envVar string) *string {
	mu.Lock()
	defer mu.Unlock()
	p, ok := regStr[envVar]
	if !ok {
		val := os.Getenv(envVar)
		if val != "" {
			noteEnvLocked(envVar, val)
		}
		p = &val
		regStr[envVar] = p
	}
	return p
}

// RegisterBool returns a pointer to the value of the named environment
// variable. If envknob.Setenv is called, the pointed-to-value will be
// updated.
func RegisterBool(envVar string) *bool {
	mu.Lock()
	defer mu.Unlock()
	p, ok := regBool[envVar]
	if !ok {
		var b bool
		p = &b
		setBoolLocked(p, envVar, os.Getenv(envVar))
		regBool[envVar] = p
	}
	return p
}

func setBoolLocked(p *bool, envVar, val string) {
	noteEnvLocked(envVar, val)
	if val == "" {
		*p = false
		return
	}
	var err error
	*p, err = strconv.ParseBool(val)
	if err != nil {
		log.Fatalf("invalid boolean environment variable %s value %q", envVar, val)
	}
}

// Bool returns the boolean value of the named environment variable.
// If the variable is not set, it returns false.
// An invalid value exits the binary with a failure.
func Bool(envVar string) bool {
	return boolOr(envVar, false)
}

// BoolDefaultTrue is like Bool, but returns true by default if the
// environment variable isn't present.
func BoolDefaultTrue(envVar string) bool {
	return boolOr(envVar, true)
}

func boolOr(envVar string, implicitValue bool) bool {
	assertNotInInit()
	val := os.Getenv(envVar)
	if val == "" {
		return implicitValue
	}
	b, err := strconv.ParseBool(val)
	if err == nil {
		noteEnv(envVar, strconv.FormatBool(b)) // canonicalize
		return b
	}
	log.Fatalf("invalid boolean environment variable %s value %q", envVar, val)
	panic("unreachable")
}

// LookupBool returns the boolean value of the named environment value.
// The ok result is whether a value was set.
// If the value isn't a valid int, it exits the program with a failure.
func LookupBool(envVar string) (v bool, ok bool) {
	assertNotInInit()
	val := os.Getenv(envVar)
	if val == "" {
		return false, false
	}
	b, err := strconv.ParseBool(val)
	if err == nil {
		return b, true
	}
	log.Fatalf("invalid boolean environment variable %s value %q", envVar, val)
	panic("unreachable")
}

// OptBool is like Bool, but returns an opt.Bool, so the caller can
// distinguish between implicitly and explicitly false.
func OptBool(envVar string) opt.Bool {
	assertNotInInit()
	b, ok := LookupBool(envVar)
	if !ok {
		return ""
	}
	var ret opt.Bool
	ret.Set(b)
	return ret
}

// LookupInt returns the integer value of the named environment value.
// The ok result is whether a value was set.
// If the value isn't a valid int, it exits the program with a failure.
func LookupInt(envVar string) (v int, ok bool) {
	assertNotInInit()
	val := os.Getenv(envVar)
	if val == "" {
		return 0, false
	}
	v, err := strconv.Atoi(val)
	if err == nil {
		noteEnv(envVar, val)
		return v, true
	}
	log.Fatalf("invalid integer environment variable %s: %v", envVar, val)
	panic("unreachable")
}

// UseWIPCode is whether TAILSCALE_USE_WIP_CODE is set to permit use
// of Work-In-Progress code.
func UseWIPCode() bool { return Bool("TAILSCALE_USE_WIP_CODE") }

// CanSSHD is whether the Tailscale SSH server is allowed to run.
//
// If disabled, the SSH server won't start (won't intercept port 22)
// if already enabled and any attempt to re-enable it will result in
// an error.
func CanSSHD() bool { return !Bool("TS_DISABLE_SSH_SERVER") }

// SSHPolicyFile returns the path, if any, to the SSHPolicy JSON file for development.
func SSHPolicyFile() string { return String("TS_DEBUG_SSH_POLICY_FILE") }

// SSHIgnoreTailnetPolicy is whether to ignore the Tailnet SSH policy for development.
func SSHIgnoreTailnetPolicy() bool { return Bool("TS_DEBUG_SSH_IGNORE_TAILNET_POLICY") }

// NoLogsNoSupport reports whether the client's opted out of log uploads and
// technical support.
func NoLogsNoSupport() bool {
	return Bool("TS_NO_LOGS_NO_SUPPORT")
}

// SetNoLogsNoSupport enables no-logs-no-support mode.
func SetNoLogsNoSupport() {
	os.Setenv("TS_NO_LOGS_NO_SUPPORT", "true")
}

var inMain atomic.Bool

// SetInMain is a hint from the caller that the main func has started so we
// don't need to do any more init-time defensive checks.
func SetInMain() {
	inMain.Store(true)
}

func assertNotInInit() {
	if inMain.Load() {
		return
	}
	skip := 0
	for {
		pc, _, _, ok := runtime.Caller(skip)
		if !ok {
			return
		}
		fu := runtime.FuncForPC(pc)
		if fu == nil {
			return
		}
		if strings.HasSuffix(fu.Name(), ".init") {
			panic("envknob check of called from init function")
		}
		skip++
	}
}
