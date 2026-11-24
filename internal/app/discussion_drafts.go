package app

import (
	"fmt"
	"slices"
	"time"
)

type discDraftContent struct {
	text       string
	targets    []string // @ 的成员 ID 列表（有序去重）
	autoTarget string   // 引用成员消息时自动预选的成员 ID；手动修改接收者后清掉
	quote      string   // 引用的消息 ID
	atts       []string
	answerQ    string // 正在回答的问题（任务消息 ID）
	mention    string // @ 的人：只提醒，不调用他的 Agent
}

func (d discDraftContent) equal(other discDraftContent) bool {
	return d.text == other.text && slices.Equal(d.targets, other.targets) && d.quote == other.quote &&
		d.answerQ == other.answerQ && d.mention == other.mention && slices.Equal(d.atts, other.atts)
}

func (d *discDraftContent) hasTarget(id string) bool {
	return id != "" && slices.Contains(d.targets, id)
}

func (d *discDraftContent) addTarget(id string, limit int) {
	if id == "" || slices.Contains(d.targets, id) {
		return
	}
	if limit > 0 && len(d.targets) >= limit {
		return
	}
	d.targets = append(slices.Clone(d.targets), id)
	d.mention = ""
	d.autoTarget = ""
}

func (d *discDraftContent) removeTarget(id string) {
	if i := slices.Index(d.targets, id); i >= 0 {
		d.targets = slices.Delete(slices.Clone(d.targets), i, i+1)
		d.autoTarget = ""
	}
}

func (d *discDraftContent) toggleTarget(id string, limit int) {
	if d.hasTarget(id) {
		d.removeTarget(id)
	} else {
		d.addTarget(id, limit)
	}
}

func (d *discDraftContent) clearRecipients() {
	d.targets, d.mention, d.autoTarget = nil, "", ""
}

func (d *discDraftContent) setMention(fp string) {
	d.mention, d.targets, d.autoTarget = fp, nil, ""
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
	content.targets = slices.Clone(content.targets)
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
				// 如果是引用自动预选出来的成员，发送完取消引用时也顺手恢复，避免下一句误发给它
				if d.autoTarget != "" && len(d.targets) == 1 && d.targets[0] == d.autoTarget {
					d.targets = nil
				}
				d.autoTarget = ""
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
