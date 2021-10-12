package wd

import (
	"log"
	"net/http"
	"path/filepath"
	"strings"
	"time"
)

type Manifest struct {
	Command []string
	Async   bool
	Timeout time.Duration
	Retries uint
	Delay   time.Duration
}

func (m *Manifest) Binary() string {
	return m.Command[0]
}

func (m *Manifest) Args() []string {
	return m.Command[1:]
}

type Runner interface {
	// Command to execute. Returns nil if not applicable. Default manifest should be used as base.
	Command(req *http.Request, defaultManifest Manifest) *Manifest
}

type RunnerFunc func(req *http.Request, defaultManifest Manifest) *Manifest

func (r RunnerFunc) Command(req *http.Request, defaultManifest Manifest) *Manifest {
	return r(req, defaultManifest)
}

func StaticScript(command string, args ...string) RunnerFunc {
	cli := append([]string{command}, args...)
	return func(req *http.Request, d Manifest) *Manifest {
		d.Command = cli
		return &d
	}
}

const (
	AttrAsync   = "user.webhook.async"   // boolean (true/false), forces async execution for script
	AttrTimeout = "user.webhook.timeout" // duration, maximum execution time
	AttrDelay   = "user.webhook.delay"   // duration, interval between attempts
	AttrRetries = "user.webhook.retries" // int64, maximum number of additional attempts
)

type DirectoryRunner struct {
	AllowDotFiles bool   // allows run scripts with leading dot in names
	ScriptsDir    string // path to directory with scripts. MUST be absolute
}

func (dr *DirectoryRunner) Command(req *http.Request, defaultManifest Manifest) *Manifest {
	absScriptPath, err := filepath.Abs(filepath.Join(dr.ScriptsDir, req.URL.Path))
	if err != nil {
		log.Println("failed detect absolute path:", err)
		return nil
	}

	if !strings.HasPrefix(absScriptPath, dr.ScriptsDir+string(filepath.Separator)) {
		log.Println("attempt to reach file outside of script dir:", absScriptPath)
		return nil
	}

	if !dr.isPathAllowed(absScriptPath) {
		log.Println("attempt to reach dot files:", absScriptPath)
		return nil
	}

	defaultManifest.Command = []string{absScriptPath}
	if err := readAttrs(absScriptPath, &defaultManifest); err != nil {
		log.Println("failed read x-attrs:", err)
	}

	return &defaultManifest
}

func (dr *DirectoryRunner) isPathAllowed(scriptPath string) bool {
	if dr.AllowDotFiles {
		return true
	}
	relPath, err := filepath.Rel(dr.ScriptsDir, scriptPath)
	if err != nil {
		log.Println("detect relative path:", err)
		return false
	}

	for _, part := range strings.Split(relPath, string(filepath.Separator)) {
		if strings.HasPrefix(part, ".") {
			return false
		}
	}
	return true
}
