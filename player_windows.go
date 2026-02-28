//go:build windows

package main

import (
	"context"
	"errors"
	"io"
	"os/exec"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

type player struct {
	mu     sync.Mutex
	cmd    *exec.Cmd
	cancel context.CancelFunc
	job    windows.Handle
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

	// 靜音（不輸出到你的 TUI）
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	cmd.Stdin = nil

	if err := cmd.Start(); err != nil {
		cancel()
		return nil, err
	}

	// 1) 建立 Job Object
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		cancel()
		return nil, err
	}

	// 2) 設定：關閉 job 時自動殺掉 job 內所有 processes（含子行程）
	var info windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE

	_, err = windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
	)
	if err != nil {
		windows.CloseHandle(job)
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		cancel()
		return nil, err
	}

	// 3) 用 PID 取得真正可用的 HANDLE
	pid := uint32(cmd.Process.Pid)
	hProcess, err := windows.OpenProcess(windows.PROCESS_ALL_ACCESS, false, pid)
	if err != nil {
		windows.CloseHandle(job)
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		cancel()
		return nil, errors.New("OpenProcess failed: " + err.Error())
	}
	defer windows.CloseHandle(hProcess)

	// 4) Assign process 到 job
	if err := windows.AssignProcessToJobObject(job, hProcess); err != nil {
		windows.CloseHandle(job)
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		cancel()
		return nil, errors.New("AssignProcessToJobObject failed: " + err.Error())
	}

	return &player{
		cmd:    cmd,
		cancel: cancel,
		job:    job,
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

	// 串行化 Stop，並先把欄位取出後清空（避免其他 goroutine 看到舊狀態）
	p.mu.Lock()
	cmd := p.cmd
	cancel := p.cancel
	job := p.job
	p.cmd = nil
	p.cancel = nil
	p.job = 0
	p.mu.Unlock()

	if cmd == nil {
		return nil
	}

	if cancel != nil {
		cancel()
	}

	// 關閉 job -> 直接終止整棵 process tree（只影響這個 job 內的 processes）
	if job != 0 {
		_ = windows.CloseHandle(job)
	}

	// 等一下讓 OS 收尾（避免殘留音訊裝置佔用）
	done := make(chan struct{}, 1)
	go func() {
		_ = cmd.Wait()
		done <- struct{}{}
	}()

	select {
	case <-done:
	case <-time.After(1200 * time.Millisecond):
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}

	return nil
}
