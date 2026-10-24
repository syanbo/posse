//go:build darwin && arm64

#include "textflag.h"

TEXT ·readWord(SB), NOSPLIT, $0-16
	MOVD addr+0(FP), R0
	MOVD (R0), R1
	MOVD R1, ret+8(FP)
	RET
