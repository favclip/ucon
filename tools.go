// +build tools

package ucon

// from https://github.com/golang/go/issues/25922#issuecomment-412992431

import (
	_ "github.com/favclip/jwg"
	_ "github.com/favclip/qbg"
	_ "golang.org/x/lint/golint"
	_ "golang.org/x/tools/cmd/goimports"
)
