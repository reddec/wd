package wd_test

import (
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/pkg/xattr"
	"github.com/reddec/wd"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_defaults(t *testing.T) {
	wh := wd.New(wd.Config{}, wd.StaticScript("echo", "-n", "123"))

	req := httptest.NewRequest(http.MethodPost, "/", nil)
	res := httptest.NewRecorder()
	wh.ServeHTTP(res, req)
	assert.Equal(t, http.StatusOK, res.Code)
	assert.Equal(t, "123", res.Body.String())
}

func Test_scriptRunner(t *testing.T) {
	env := New()
	defer env.Clear()

	script := env.Script("echo -n 123")

	wh := wd.New(wd.Config{}, &wd.DirectoryRunner{
		ScriptsDir: env.dir,
	})

	req := httptest.NewRequest(http.MethodPost, "/"+script, nil)
	res := httptest.NewRecorder()
	wh.ServeHTTP(res, req)
	assert.Equal(t, http.StatusOK, res.Code)
	assert.Equal(t, "123", res.Body.String())

	t.Run("override xattr", func(t *testing.T) {
		err := xattr.Set(env.Path(script), wd.AttrAsync, []byte("forced"))
		require.NoError(t, err)

		req := httptest.NewRequest(http.MethodPost, "/"+script, nil)
		res := httptest.NewRecorder()
		wh.ServeHTTP(res, req)
		assert.Equal(t, http.StatusAccepted, res.Code)
		assert.Empty(t, res.Body.String())
	})
}

type testEnv struct {
	dir string
}

func New() *testEnv {
	tmpDir, err := ioutil.TempDir("", "")
	if err != nil {
		panic(err)
	}
	return &testEnv{dir: tmpDir}
}

func (te *testEnv) Clear() {
	_ = os.RemoveAll(te.dir)
}

func (te *testEnv) Script(content string) string {
	f, err := ioutil.TempFile(te.dir, "")
	if err != nil {
		panic(err)
	}
	if _, err := f.WriteString("#!/bin/bash\nset -e\n" + content); err != nil {
		panic(err)
	}
	err = f.Close()
	if err != nil {
		panic(err)
	}
	if err := os.Chmod(f.Name(), 0755); err != nil {
		panic(err)
	}
	name, err := filepath.Rel(te.dir, f.Name())
	if err != nil {
		panic(err)
	}
	return name
}

func (te *testEnv) Path(name string) string {
	return filepath.Join(te.dir, name)
}
