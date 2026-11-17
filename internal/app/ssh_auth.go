package app

import (
	"context"
	"slices"
	"sync"

	"github.com/egoist/mygo/ui"

	"posse/internal/remote"
)

type sshAuthResult struct {
	answer string
	err    error
}

type sshAuthPrompt struct {
	ctx    context.Context
	host   string
	prompt remote.Prompt
	cancel context.CancelFunc
	reply  chan sshAuthResult
	value  string // 只由界面线程读写，不进入持久化状态。
}

// SSH 会并发发来提示或提前结束安全密钥通知；队列按请求对象删除，旧取消不影响下一项。
type sshAuthentication struct {
	mu     sync.Mutex
	queue  []*sshAuthPrompt
	closed bool
	wake   func()
}

func (s *sshAuthentication) ask(ctx context.Context, host string, p remote.Prompt, cancel context.CancelFunc) (string, error) {
	q := &sshAuthPrompt{ctx: ctx, host: host, prompt: p, cancel: cancel, reply: make(chan sshAuthResult, 1)}
	s.mu.Lock()
	if err := ctx.Err(); err != nil {
		s.mu.Unlock()
		return "", err
	}
	if s.closed {
		s.mu.Unlock()
		return "", context.Canceled
	}
	s.queue = append(s.queue, q)
	s.mu.Unlock()
	s.changed()
	defer func() {
		s.mu.Lock()
		if i := slices.Index(s.queue, q); i >= 0 {
			s.queue = slices.Delete(s.queue, i, i+1)
		}
		s.mu.Unlock()
		s.changed()
	}()
	select {
	case r := <-q.reply:
		if err := ctx.Err(); err != nil {
			return "", err
		}
		return r.answer, r.err
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func (s *sshAuthentication) changed() {
	if s.wake != nil {
		s.wake()
	}
}

func (s *sshAuthentication) current() *sshAuthPrompt {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, q := range s.queue {
		if q.ctx.Err() == nil {
			return q
		}
	}
	return nil
}

func (s *sshAuthentication) finish(q *sshAuthPrompt, answer string, err error) {
	s.mu.Lock()
	i := slices.Index(s.queue, q)
	if i < 0 || q.ctx.Err() != nil {
		s.mu.Unlock()
		return
	}
	s.queue = slices.Delete(s.queue, i, i+1)
	s.mu.Unlock()
	// 返回 askpass 失败还可能触发 SSH 的下一次尝试；用户取消要结束整次建连。
	if err != nil && q.cancel != nil {
		q.cancel()
	}
	q.reply <- sshAuthResult{answer, err}
	s.changed()
}

func (s *sshAuthentication) cancelPending(close bool) {
	s.mu.Lock()
	s.closed = s.closed || close
	pending := s.queue
	s.queue = nil
	s.mu.Unlock()
	for _, q := range pending {
		if q.cancel != nil {
			q.cancel()
		}
		q.reply <- sshAuthResult{err: context.Canceled}
	}
	s.changed()
}

func (a *app) sshAuthDialog(c *ui.Context) {
	s := a.windowOwner().sshAuth
	if s == nil {
		return
	}
	q := s.current()
	if q == nil {
		return
	}
	open := true
	ui.Modal(c.Key(q), &open, func() {
		ui.Column(c).Width(sshAuthWidth()).Gap(12).Children(func() {
			titleText(c, L(c).SshAuthTitle(q.host), fsTitle)
			ui.Text(c, q.prompt.Text).FontSize(fsBody).LineHeight(1.45).Wrap()
			var submitted bool
			if !q.prompt.Confirm && !q.prompt.Notify {
				submitted = fieldInput(c, &q.value, "").Password().Label(L(c).SshAuthAnswer()).AutoFocus().Submitted()
			}
			ui.Row(c).Gap(8).Justify(ui.End).Padding(8, 0, 0, 0).Children(func() {
				cancel := confirmButton(c, pressable(textButton(c, L(c).CommonCancel())))
				if q.prompt.Confirm || q.prompt.Notify {
					cancel.AutoFocus()
				}
				if cancel.Clicked() {
					open = false
				}
				if !q.prompt.Notify {
					verb := L(c).CommonContinue()
					if q.prompt.Confirm {
						verb = L(c).SshAuthTrust()
					}
					if confirmButton(c, pressable(accentButton(c, verb))).Clicked() {
						submitted = true
					}
				}
			})
			if open && submitted {
				answer := q.value
				if q.prompt.Confirm {
					answer = "yes"
				}
				s.finish(q, answer, nil)
				q.value = ""
			}
		})
	})
	if !open {
		s.finish(q, "", context.Canceled)
		q.value = ""
	}
}
