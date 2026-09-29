//go:build windows

package main

import "os/exec"

func ownProcessGroup(cmd *exec.Cmd) {}

func killGroup(cmd *exec.Cmd) {}
