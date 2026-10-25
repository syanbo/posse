# Name this session (posse session title)

Use only when the user asks to name or title the current session. The title is for the user, so write it in Chinese.

## 1. Draft the title

Format `MMDD｜类型｜摘要`, three parts separated by the full-width bar `｜`.

- MMDD is today's date; posse rewrites it to the local date anyway.
- 类型 is exactly one of: 修复 feat 调研 调试 重构 文档 杂项.
- 摘要 is Chinese, at most 20 characters, saying what this session is doing. Proper nouns and file names may stay in English. Do not start it with the 类型 word.

What the user gave you:
- nothing: summarize the user's requests in this session so far, following the main task, not a passing detour;
- a full title: use it as is;
- only a summary or keywords: use it as 摘要 and pick the 类型 yourself.

## 2. Set it

```bash
posse session title '<title>'
```

It must run inside the Posse terminal of this session (it reads POSSE_TERMINAL_ID). Do not rename the session any other way.
If your sandbox blocks the command, ask to run it outside the sandbox.

## 3. Report

On success it prints the final title and where it takes effect; tell the user both in one sentence.
If it fails saying the format is wrong or the summary is too long, fix the title by the rules above and run it once more; otherwise relay the error as is.
