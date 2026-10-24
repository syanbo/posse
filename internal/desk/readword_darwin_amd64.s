//go:build darwin && amd64

#include "textflag.h"

TEXT ·readWord(SB), NOSPLIT, $0-16
	MOVQ addr+0(FP), AX
	MOVQ (AX), AX
	MOVQ AX, ret+8(FP)
	RET
