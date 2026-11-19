// Code generated from PostgreSQL's contrib/fuzzystrmatch/daitch_mokotoff_header.pl
// output (converted to Go). DO NOT EDIT.

package main

var codes_0_1_X = [2]dmCodes{{"0", "1", "X"}}
var codes_0_7_X = [2]dmCodes{{"0", "7", "X"}}
var codes_0_X_X = [2]dmCodes{{"0", "X", "X"}}
var codes_1_1_X = [2]dmCodes{{"1", "1", "X"}}
var codes_1_X_X = [2]dmCodes{{"1", "X", "X"}}
var codes_1_X_X_or_4_4_4 = [2]dmCodes{{"1", "X", "X"}, {"4", "4", "4"}}
var codes_2_43_43 = [2]dmCodes{{"2", "43", "43"}}
var codes_2_4_4 = [2]dmCodes{{"2", "4", "4"}}
var codes_3_3_3 = [2]dmCodes{{"3", "3", "3"}}
var codes_3_3_3_or_4_4_4 = [2]dmCodes{{"3", "3", "3"}, {"4", "4", "4"}}
var codes_4_4_4 = [2]dmCodes{{"4", "4", "4"}}
var codes_5_54_54 = [2]dmCodes{{"5", "54", "54"}}
var codes_5_5_5 = [2]dmCodes{{"5", "5", "5"}}
var codes_5_5_5_or_45_45_45 = [2]dmCodes{{"5", "5", "5"}, {"45", "45", "45"}}
var codes_5_5_5_or_4_4_4 = [2]dmCodes{{"5", "5", "5"}, {"4", "4", "4"}}
var codes_5_5_X = [2]dmCodes{{"5", "5", "X"}}
var codes_66_66_66 = [2]dmCodes{{"66", "66", "66"}}
var codes_6_6_6 = [2]dmCodes{{"6", "6", "6"}}
var codes_7_7_7 = [2]dmCodes{{"7", "7", "7"}}
var codes_8_8_8 = [2]dmCodes{{"8", "8", "8"}}
var codes_94_94_94_or_4_4_4 = [2]dmCodes{{"94", "94", "94"}, {"4", "4", "4"}}
var codes_9_9_9 = [2]dmCodes{{"9", "9", "9"}}
var codes_X_X_6_or_X_X_X = [2]dmCodes{{"X", "X", "6"}, {"X", "X", "X"}}

var letter_A = []dmLetter{
	{'I', nil, &codes_0_1_X},
	{'J', nil, &codes_0_1_X},
	{'U', nil, &codes_0_7_X},
	{'Y', nil, &codes_0_1_X},
}

var letter_CH = []dmLetter{
	{'S', nil, &codes_5_54_54},
}

var letter_CS = []dmLetter{
	{'Z', nil, &codes_4_4_4},
}

var letter_CZ = []dmLetter{
	{'S', nil, &codes_4_4_4},
}

var letter_C = []dmLetter{
	{'H', letter_CH, &codes_5_5_5_or_4_4_4},
	{'K', nil, &codes_5_5_5_or_45_45_45},
	{'S', letter_CS, &codes_4_4_4},
	{'Z', letter_CZ, &codes_4_4_4},
}

var letter_DR = []dmLetter{
	{'S', nil, &codes_4_4_4},
	{'Z', nil, &codes_4_4_4},
}

var letter_DS = []dmLetter{
	{'H', nil, &codes_4_4_4},
	{'Z', nil, &codes_4_4_4},
}

var letter_DZ = []dmLetter{
	{'H', nil, &codes_4_4_4},
	{'S', nil, &codes_4_4_4},
}

var letter_D = []dmLetter{
	{'R', letter_DR, nil},
	{'S', letter_DS, &codes_4_4_4},
	{'T', nil, &codes_3_3_3},
	{'Z', letter_DZ, &codes_4_4_4},
}

var letter_E = []dmLetter{
	{'I', nil, &codes_0_1_X},
	{'J', nil, &codes_0_1_X},
	{'U', nil, &codes_1_1_X},
	{'Y', nil, &codes_0_1_X},
}

var letter_F = []dmLetter{
	{'B', nil, &codes_7_7_7},
}

var letter_I = []dmLetter{
	{'A', nil, &codes_1_X_X},
	{'E', nil, &codes_1_X_X},
	{'O', nil, &codes_1_X_X},
	{'U', nil, &codes_1_X_X},
}

var letter_K = []dmLetter{
	{'H', nil, &codes_5_5_5},
	{'S', nil, &codes_5_54_54},
}

var letter_M = []dmLetter{
	{'N', nil, &codes_66_66_66},
}

var letter_N = []dmLetter{
	{'M', nil, &codes_66_66_66},
}

var letter_O = []dmLetter{
	{'I', nil, &codes_0_1_X},
	{'J', nil, &codes_0_1_X},
	{'Y', nil, &codes_0_1_X},
}

var letter_P = []dmLetter{
	{'F', nil, &codes_7_7_7},
	{'H', nil, &codes_7_7_7},
}

var letter_R = []dmLetter{
	{'S', nil, &codes_94_94_94_or_4_4_4},
	{'Z', nil, &codes_94_94_94_or_4_4_4},
}

var letter_SCHTC = []dmLetter{
	{'H', nil, &codes_2_4_4},
}

var letter_SCHTSC = []dmLetter{
	{'H', nil, &codes_2_4_4},
}

var letter_SCHTS = []dmLetter{
	{'C', letter_SCHTSC, nil},
	{'H', nil, &codes_2_4_4},
}

var letter_SCHT = []dmLetter{
	{'C', letter_SCHTC, nil},
	{'S', letter_SCHTS, nil},
}

var letter_SCH = []dmLetter{
	{'D', nil, &codes_2_43_43},
	{'T', letter_SCHT, &codes_2_43_43},
}

var letter_SC = []dmLetter{
	{'H', letter_SCH, &codes_4_4_4},
}

var letter_SHC = []dmLetter{
	{'H', nil, &codes_2_4_4},
}

var letter_SHTC = []dmLetter{
	{'H', nil, &codes_2_4_4},
}

var letter_SHTS = []dmLetter{
	{'H', nil, &codes_2_4_4},
}

var letter_SHT = []dmLetter{
	{'C', letter_SHTC, nil},
	{'S', letter_SHTS, nil},
}

var letter_SH = []dmLetter{
	{'C', letter_SHC, nil},
	{'D', nil, &codes_2_43_43},
	{'T', letter_SHT, &codes_2_43_43},
}

var letter_STC = []dmLetter{
	{'H', nil, &codes_2_4_4},
}

var letter_STR = []dmLetter{
	{'S', nil, &codes_2_4_4},
	{'Z', nil, &codes_2_4_4},
}

var letter_STSC = []dmLetter{
	{'H', nil, &codes_2_4_4},
}

var letter_STS = []dmLetter{
	{'C', letter_STSC, nil},
	{'H', nil, &codes_2_4_4},
}

var letter_ST = []dmLetter{
	{'C', letter_STC, nil},
	{'R', letter_STR, nil},
	{'S', letter_STS, nil},
}

var letter_SZC = []dmLetter{
	{'S', nil, &codes_2_4_4},
	{'Z', nil, &codes_2_4_4},
}

var letter_SZ = []dmLetter{
	{'C', letter_SZC, nil},
	{'D', nil, &codes_2_43_43},
	{'T', nil, &codes_2_43_43},
}

var letter_S = []dmLetter{
	{'C', letter_SC, &codes_2_4_4},
	{'D', nil, &codes_2_43_43},
	{'H', letter_SH, &codes_4_4_4},
	{'T', letter_ST, &codes_2_43_43},
	{'Z', letter_SZ, &codes_4_4_4},
}

var letter_TC = []dmLetter{
	{'H', nil, &codes_4_4_4},
}

var letter_TR = []dmLetter{
	{'S', nil, &codes_4_4_4},
	{'Z', nil, &codes_4_4_4},
}

var letter_TSC = []dmLetter{
	{'H', nil, &codes_4_4_4},
}

var letter_TS = []dmLetter{
	{'C', letter_TSC, nil},
	{'H', nil, &codes_4_4_4},
	{'Z', nil, &codes_4_4_4},
}

var letter_TTC = []dmLetter{
	{'H', nil, &codes_4_4_4},
}

var letter_TTSC = []dmLetter{
	{'H', nil, &codes_4_4_4},
}

var letter_TTS = []dmLetter{
	{'C', letter_TTSC, nil},
	{'Z', nil, &codes_4_4_4},
}

var letter_TT = []dmLetter{
	{'C', letter_TTC, nil},
	{'S', letter_TTS, &codes_4_4_4},
	{'Z', nil, &codes_4_4_4},
}

var letter_TZ = []dmLetter{
	{'S', nil, &codes_4_4_4},
}

var letter_T = []dmLetter{
	{'C', letter_TC, &codes_4_4_4},
	{'H', nil, &codes_3_3_3},
	{'R', letter_TR, nil},
	{'S', letter_TS, &codes_4_4_4},
	{'T', letter_TT, nil},
	{'Z', letter_TZ, &codes_4_4_4},
}

var letter_U = []dmLetter{
	{'E', nil, &codes_0_1_X},
	{'I', nil, &codes_0_1_X},
	{'J', nil, &codes_0_1_X},
	{'Y', nil, &codes_0_1_X},
}

var letter_ZDZ = []dmLetter{
	{'H', nil, &codes_2_4_4},
}

var letter_ZD = []dmLetter{
	{'Z', letter_ZDZ, &codes_2_4_4},
}

var letter_ZHDZ = []dmLetter{
	{'H', nil, &codes_2_4_4},
}

var letter_ZHD = []dmLetter{
	{'Z', letter_ZHDZ, nil},
}

var letter_ZH = []dmLetter{
	{'D', letter_ZHD, &codes_2_43_43},
}

var letter_ZSC = []dmLetter{
	{'H', nil, &codes_4_4_4},
}

var letter_ZS = []dmLetter{
	{'C', letter_ZSC, nil},
	{'H', nil, &codes_4_4_4},
}

var letter_Z = []dmLetter{
	{'D', letter_ZD, &codes_2_43_43},
	{'H', letter_ZH, &codes_4_4_4},
	{'S', letter_ZS, &codes_4_4_4},
}

var letter_ = []dmLetter{
	{'A', letter_A, &codes_0_X_X},
	{'B', nil, &codes_7_7_7},
	{'C', letter_C, &codes_5_5_5_or_4_4_4},
	{'D', letter_D, &codes_3_3_3},
	{'E', letter_E, &codes_0_X_X},
	{'F', letter_F, &codes_7_7_7},
	{'G', nil, &codes_5_5_5},
	{'H', nil, &codes_5_5_X},
	{'I', letter_I, &codes_0_X_X},
	{'J', nil, &codes_1_X_X_or_4_4_4},
	{'K', letter_K, &codes_5_5_5},
	{'L', nil, &codes_8_8_8},
	{'M', letter_M, &codes_6_6_6},
	{'N', letter_N, &codes_6_6_6},
	{'O', letter_O, &codes_0_X_X},
	{'P', letter_P, &codes_7_7_7},
	{'Q', nil, &codes_5_5_5},
	{'R', letter_R, &codes_9_9_9},
	{'S', letter_S, &codes_4_4_4},
	{'T', letter_T, &codes_3_3_3},
	{'U', letter_U, &codes_0_X_X},
	{'V', nil, &codes_7_7_7},
	{'W', nil, &codes_7_7_7},
	{'X', nil, &codes_5_54_54},
	{'Y', nil, &codes_1_X_X},
	{'Z', letter_Z, &codes_4_4_4},
	{'a', nil, &codes_X_X_6_or_X_X_X},
	{'e', nil, &codes_X_X_6_or_X_X_X},
	{'t', nil, &codes_3_3_3_or_4_4_4},
}
