//go:build !windows

package wd

import (
	"fmt"
	"strconv"
	"time"

	"github.com/pkg/xattr"
)

func readAttrs(file string, manifest *Manifest) error {
	names, err := xattr.List(file)
	if err != nil {
		return fmt.Errorf("list attrs: %w", err)
	}
	for _, name := range names {
		switch name {
		case AttrAsync:
			var mode AsyncMode
			if data, err := xattr.Get(file, name); err != nil {
				return fmt.Errorf("read %s: %w", name, err)
			} else if err := mode.UnmarshalText(data); err != nil {
				return fmt.Errorf("parse %s as async mode: %w", name, err)
			} else {
				manifest.Async = mode
			}
		case AttrTimeout:
			if data, err := xattr.Get(file, name); err != nil {
				return fmt.Errorf("read %s: %w", name, err)
			} else if v, err := time.ParseDuration(string(data)); err != nil {
				return fmt.Errorf("parse %s as duration: %w", name, err)
			} else {
				manifest.Timeout = v
			}
		case AttrDelay:
			if data, err := xattr.Get(file, name); err != nil {
				return fmt.Errorf("read %s: %w", name, err)
			} else if v, err := time.ParseDuration(string(data)); err != nil {
				return fmt.Errorf("parse %s as duration: %w", name, err)
			} else {
				manifest.Delay = v
			}
		case AttrRetries:
			if data, err := xattr.Get(file, name); err != nil {
				return fmt.Errorf("read %s: %w", name, err)
			} else if v, err := strconv.ParseUint(string(data), 10, 64); err != nil {
				return fmt.Errorf("parse %s as duration: %w", name, err)
			} else {
				manifest.Retries = uint(v)
			}
		}
	}
	return nil
}
