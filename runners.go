package wd

import (
	"log"
	"net/http"
	"path/filepath"
	"strings"
)

type Runner interface {
	// Command to execute. Return nil if not applicable.
	Command(req *http.Request) []string
}

type RunnerFunc func(req *http.Request) []string

func (r RunnerFunc) Command(req *http.Request) []string {
	return r(req)
}

func StaticScript(command string, args ...string) RunnerFunc {
	cli := append([]string{command}, args...)
	return RunnerFunc(func(req *http.Request) []string {
		return cli
	})
}

type DirectoryRunner struct {
	AllowDotFiles bool   // allows run scripts with leading dot in names
	ScriptsDir    string // path to directory with scripts. MUST be absolute
}

func (dr *DirectoryRunner) Command(req *http.Request) []string {
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

	return []string{absScriptPath}
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
