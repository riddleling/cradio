//go:build !windows

package main

import (
	"context"
	"os/exec"
	"sync"
)

type player struct {
	mu     sync.Mutex
	cmd    *exec.Cmd
	cancel context.CancelFunc
}

func startMPV(url string) (*player, error) {
	mpvPath, err := resolveMPVPath()
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithCancel(context.Background())

	cmd := exec.CommandContext(ctx, mpvPath,
		"--no-video",
		"--no-ytdl",
		"--force-window=no",
		"--audio-display=no",
		"--cache=yes",
		"--cache-secs=15",
		"--really-quiet",
		url,
	)

	cmd.Stdout = nil
	cmd.Stderr = nil

	if err := cmd.Start(); err != nil {
		cancel()
		return nil, err
	}

	return &player{
		cmd:    cmd,
		cancel: cancel,
	}, nil
}

func (p *player) IsPlaying() bool {
	if p == nil {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.cmd != nil && p.cmd.Process != nil
}

func (p *player) Stop() error {
	if p == nil {
		return nil
	}

	p.mu.Lock()
	cmd := p.cmd
	cancel := p.cancel
	p.cmd = nil
	p.cancel = nil
	p.mu.Unlock()

	if cmd == nil {
		return nil
	}

	if cancel != nil {
		cancel()
	}

	_ = cmd.Process.Kill()
	_ = cmd.Wait()

	return nil
}
