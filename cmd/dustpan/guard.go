package main

import (
	"errors"
	"path/filepath"

	"github.com/Audarya07/dustpan/internal/env"
)

// checkEnv refuses to scan or clean when the environment is too broken to
// apply the never-touch rules: they are anchored on an absolute home folder
// and the temp folders.
func checkEnv(e env.Env) error {
	if !filepath.IsAbs(e.Home) {
		return errors.New("can't find your home folder (is $HOME set?); refusing to run")
	}
	if len(e.TmpDirs) == 0 {
		return errors.New("can't find the temp folders; refusing to run")
	}
	return nil
}
