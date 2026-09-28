//go:build !darwin && !windows

package cmd

import "context"

func watchDesktopParent(ctx context.Context, stop context.CancelFunc) {}
