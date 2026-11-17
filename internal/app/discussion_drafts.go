package app

import (
	"fmt"
	"slices"
	"time"
)

type discDraftContent struct {
	text    string
	target  string // @ 的成员 ID
	quote   string // 引用的消息 ID
	atts    []string
	answerQ string // 正在回答的问题（任务消息 ID）
	mention string // @ 的人：只提醒，不调用他的 Agent
}

func (d discDraftContent) equal(other discDraftContent) bool {
	return d.text == other.text && d.target == other.target && d.quote == other.quote &&
		d.answerQ == other.answerQ && d.mention == other.mention && slices.Equal(d.atts, other.atts)
}

type discDraft struct {
	discDraftContent
	clientID string // 同一份内容失败后重试沿用；编辑后换 ID，避免被去重成旧消息。
	sending  bool
	request  *discDraftRequest
}

type discDraftRequest struct {
	content  discDraftContent
	clientID string
	question string
	body     string
	typed    bool
}

func (b *discBoard) saveDraft() {
	if b.sel == "" {
		return
	}
	if b.drafts == nil {
		b.drafts = make(map[string]discDraft)
	}
	b.drafts[b.sel] = b.discDraft
}

// editDraft 只在界面线程调用；后台任务按发起时的讨论定位，不能借用当前输入区。
func (b *discBoard) editDraft(key string, edit func(*discDraft)) {
	if b.sel == key {
		edit(&b.discDraft)
		return
	}
	d, ok := b.drafts[key]
	if !ok {
		return
	}
	edit(&d)
	b.drafts[key] = d
}

func (d *discDraft) beginSend(question, body string, typed bool) *discDraftRequest {
	old := d.request
	if d.clientID == "" || old != nil && (!d.discDraftContent.equal(old.content) ||
		old.question != question || old.body != body || old.typed != typed) {
		d.clientID = fmt.Sprintf("c_%x", time.Now().UnixNano())
	}
	content := d.discDraftContent
	content.atts = slices.Clone(content.atts)
	r := &discDraftRequest{content: content, clientID: d.clientID, question: question, body: body, typed: typed}
	d.request, d.sending = r, true
	return r
}

func (b *discBoard) finishSend(key string, request *discDraftRequest, success bool) {
	b.editDraft(key, func(d *discDraft) {
		if d.request != request {
			return
		}
		d.sending = false
		if !success {
			return
		}
		d.clientID, d.request = "", nil
		// 发送中仍能编辑；只清掉没有改过的输入，附件也只清理这一份。
		if request.question == "" {
			if d.discDraftContent.equal(request.content) {
				removePasted(d.atts)
				d.text, d.quote, d.atts, d.mention = "", "", nil, ""
			}
		} else if request.typed {
			content := d.discDraftContent
			// 已回答的轮询可能先于 RPC 回复退出回答模式，不算用户改写了正文。
			if content.answerQ == "" {
				content.answerQ = request.content.answerQ
			}
			if content.equal(request.content) {
				d.text, d.answerQ = "", ""
			}
		} else if d.answerQ == request.question {
			d.answerQ = ""
		}
	})
}
