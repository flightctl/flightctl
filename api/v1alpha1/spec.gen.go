// Package v1alpha1 provides primitives to interact with the openapi HTTP API.
//
// Code generated by github.com/oapi-codegen/oapi-codegen/v2 version v2.3.0 DO NOT EDIT.
package v1alpha1

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"fmt"
	"net/url"
	"path"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
)

// Base64 encoded, gzipped, json marshaled Swagger object
var swaggerSpec = []string{

	"H4sIAAAAAAAC/+x9a3PbNtb/V8FwdyZt/7KUZLM7W79znLT1v7l4bKcvnjrPDkQeSdiQAAuActSMv/sz",
	"uJEgCUqk77X5po2F+8E5B79zAfgtilmWMwpUimj/WyTiFWRY//OQ0YRIwqj6IwERc5KbP6siFDMqMaEC",
	"JSAxSQVaMI4YBYRFDrFEbIHkClBccA5UIiGxBPMjEejg+AidgGAFj2EaTaKcsxy4JKDHT7GQvwDmcg5Y",
	"npEM1I9yk0O0HwnJCV1GlxNd64xjKvR8XLX6dM9WgFQ9JEkGZj7lAmTZFhK04CzTs1fzLASSDGHK5Aq4",
	"ml5r7AyEwMvAgL8UGaaIA07wPAVk6yFCExJjSeiyJBees0LayZUzCQ7G5gL4GpKfgQLH4X1RC51mIHGC",
	"JZ4uy5pIrrBsLPwCCyRAojkWkKAiN8MuGM+wjPYjQuW/XlXzIFTCEriaCAcsQoN/N+cEFt8jU64ZoTbi",
	"M9FrnYb0qvu/c1hE+9HfZhWLzix/zkoOPDXVL11PPZudqcqXejV/FIRDEu3/7oa2XX0uJ8fm/4VYqjGa",
	"w+5/i4AWmWp8xguIJtFPOBXq/5/oF8ouqNeLXeIk+rqn2uytMac4U7z+e7Nf21fjV9d14+dyJH9+Z5YY",
	"bnYHec7ZGpJoEh3EMQhB5ik0/3CyeIy50FVPNzTW//i4Bp7iPCd0eQopxJJxRaffcEoS3RAnm2gSvSHi",
	"yzEHIQqu+nsPGeMb74fjozfeX4fHn7y/DtaYpNhM5JizpSox9HoDS44TOyEhIUs+USLFSUGpqXBolBBw",
	"77fTHGI3PfP/fjvxlnKWphlQeQJ/FCCkR7kTyJkgkvFNkGyKWp0FLdr6hSWdf0oBZAexdZlb0htYkxhK",
	"uuu/GtQ3P7b2wPxc3wnzW30/zG/+rtiWjb3RI1c7ZH4I75MdJrBbtpW3Z2eQ5SmW8BtwQRi1W3jpbXYl",
	"gvVzA+iS0IBSfqt/R9wM6nST6Qt9B9PldIJylmSYTlDMCZsgkPH3QR1Fknb3R2/Kw871Gm6bBY+MI/Vz",
	"vx4Ur7Y7+ICznu0rDVvvwRC01YejjSXcBAnJ8hwSTZ9piEANtar30yzbTn5SqVq7WyFla5iiPU/zO+KQ",
	"cxBKvSOM8tVGkBinKNGFbTSBc2JZqd3hwfGRLUMJLAgFoSmwNr9BgszxUeKWcmRzuLIFwhSZeU/RqTql",
	"uUBixYo0UWRcA5eIQ8yWlPxZ9qbRhdTIRIKQSJ2wnOIUrXFawARhmqAMbxAH1S8qqNeDriKm6D3jClUs",
	"2D5aSZmL/dlsSeT0y7/FlDB1/mUFJXIzU3vJybxQmmSWwBrSmSDLPczjFZEQy4LDDOdkT0+WqkWJaZb8",
	"jVsVJUJc9IXQgBT8SmiCiNoRU9NMtaKYk7yTt6dnyPVvqGoI6G1rRUtFB0IXwE1NDdNUL0CTnBFqoU1K",
	"NMQs5hmRapO0+lZknqJDTCmTaA6oyBMsIZmiI4oOcQbpIRZw65RU1BN7imQiDCQNZNsFXz5qEr0HibUc",
	"5xDvalEp1v7YyraxwKohzJ4cWR7wpt8txe+IkF2SrMoMz6TqX2yBzO9ilOJbl2IiIQscBe/aG1HW3M06",
	"FRqPMOd4M6qL+1EXaheNshgixG6ru4X54+mpVT116eyANkxIDoB0KaIapnD06eRdD+SgO+yeSHgaMaML",
	"suxmalNeslOduxOimmSEYsm41/fmg0ZdtnNjPk4iRuHjItr/ffs+/EzkoW52zNmaJMCtPt7e6tdiDpyC",
	"BHEKMQc5qPERTQmF0KghajaFtcR+AXidYRmvjrFUes7suiNdbn6M9qP//R3v/flZ/ef53o97/5l+/uHv",
	"IT6uD3sZmBjrqXEsR6ojzlgeQ+ad4a/vgC7lKtp/+c9/TZrrONj7n+d7P+6fn+/9Z3p+fn7+wxVXc9nN",
	"xh2A3C/14a5SPDwzx5ZxICmFJkr4jh0KRratUoiSY5LqijiWBU4rX5yrPkGgIAXBabpBxFgBpgStsEBK",
	"I2rGiCUkujDDFC8h02oUuK5IKMLoYkXSAAQvPUGBpR62HYTgYfleh0/lsNzJ0iHDB4RVDraeWsvVZlGz",
	"TwNzMTx6RBesJxSr6lccrm3rHoS01ZE6kARiV1pTy6jvXps9MA9kwFu7RQJqFAlKQVnD4gXQupwkYlYU",
	"JNE4rKDkjwIU8ybqMF1sGmttYEnvEA67Ug+8Gkr+GFecP29229IEc8bk0Zt2n68Zk+jozZCuMhyvCIVQ",
	"b+9d0aD+AIuCa5k1REiMyOD0uEacVsM2dbRLmBO5QX6nTnYN23lz8JR6rt3SdGn2NEz7j64SMrX6L7KJ",
	"dfxtLvfGp2x7Rg06fd7Bt75IBBcjap4VXyIDbBlLstZKv4MrTYW6pmx22Q6TMJxs6VMVD+wx7IVSnVHP",
	"E1Xvprk31hlUTW5SW36I7i3/bMjR16hSdxVZpK8DFFg7d3GqtAfoZtXhORqfowtpdCGJWUuchnmT2s2v",
	"4FiyM+2lEA6sTLdNAOxCOS2ecyUuRAsCXaxArsDEMJ3KUDB4DkCRq+9pxjljKWCNPV3pgewe6UC7vFTn",
	"OiiNpULN8ao23AUWoZGqTXeFrzfdA73euIF8vWxLwwGCFM8hvQ48MB3UgJr9STI1dLpxmqt1ilcby2EZ",
	"VLXmd7co9xf16GeNFqs+52BVe5CILS60LNKL1cJezWC1uoOzVWU8be7b1Rnckl7WURuSjP7PR+r/DJ+F",
	"uzWAqmb22atofC+tus8EkpgvwdrwAT+K4O0hY8HNAMdv3+8BjVkCCTr+9fD0by+eo1g1XuiDDQmy1PFv",
	"XnF5QJvX3VJXjhapqfajY4f11FFxmDesl7atQMMgWS/RxuUk8sgc2CBvD1obpTYFEn+fgvsy2IN2daW2",
	"xZkWcuO85bzmLndO164MOV3fJcbtPJVdvc+XNg2m3aH+uW7oWeSRjMHE0Z4b7bmyhZaUYTacaXKzdpvu",
	"Mwygy6I6aNY/j3J870i52odeJ4lR2CMkfqSQuFInYTneAn0Xqnwn3BU2B3bn0vAcUpcwq/nNppCGYMld",
	"5Fs1s8vDmrBRq5x0N607oLJXOAwe623oHSvWtZuhYouxvBpohddwDzFjs5hbQrn67gWJTVpFyfODMkZC",
	"qSouWb8zxrIdHXud2CYh3gknoajjM0375LC0ln45aYrVksgT1UPz9xzLVXB9vEyo3x1Jqup6Wp+hQgDC",
	"wsaaaIxMyTkNZmhoPXMCa+LAwnbCetNrNZ6YVe2UZ0uTdr3PZk8sXU8gZ+WGBJ2uC5wKaPKPmmGYdN/l",
	"TN9DUNTKmITvfQJ+OnmnaBenjII+C3daYHqgDrb6Rcr8sMy5GjD7GE9jHsB/r7GAf71CzjjmjEl0eBDa",
	"0RwLccF4EqaBKzWhvkKu0AWRK/TL2dmxuYWUMy79S09ld6Gs+S8kN2jiN+DGSA8iztMvJLc01xoOuEKb",
	"VYNQwECmohclzt6dah8Bsqdyr4mrzr/Apn/nqnLPvgsBvDsQ7Ep30b9HmohlsyuKyarGoTtS9Tx2tioq",
	"vDq9jJ+JvAHBmvgz7JCyU7G6kpDlnKyxhF9hc4yFyFccC+gWF1OuN0yI1XHZ9iFISX1Cu9jZrhudnv7S",
	"n6MvO2l/4wpazWs492gq9Gblimc62K7qLMR1nUmkN4oaiB6lm66SF7DrlLV9hE/ZrYm0N7oUofsPYqCM",
	"FVQedwGhDqBnCkSO4x4wsKo68UbbCVCqOYepVzer2g4WlOFcYbQvsJkYUz3HhAtz5RhzQAcf3ihr+W2W",
	"y82MFmlqYsHI2XXK5JDxStkKK0KXbRtAF78bHpPevm6/1xDzl5Zy0A+iSqxBOweBnEFpVi02VK5AkrjK",
	"MUdZIYxNNEGExmmRELrUni2h3UFrzAkrRGmX6WmIKTqowK4yzLRRxWi60bfU2QJ9q0zUCXITuwzaUZLQ",
	"IhSXsCW6/zlor7lN/lXnt/4bo5RkRLrsUVpkc+A6/VIZWYiDLDiFxHi2qhSK8ta51fArLFDGOGj4grC7",
	"WjpFSh8a3iECsRz/UUDpJJvreSRKMRIhdIG+kV9mSVhfm+fJwca21BYnEcZ/KJmaJiewNi8AUPgqXYSg",
	"nElF90NDFbVJWFmwggipbE3dl5qWdQZZlA2OZHalxiQr7O17te54hekSEsS4IYFcYWX2LuACZYQWilx6",
	"c9XZpMTlTJs8ZuudB3NBIE1KaqOLFVBUCOMQIwKVO2lIeUHSVE3RJMPGJslNVpQ2e7kgXCfIiZxRARNU",
	"0BSEQBtWmPlwiIGUpJTsC1DjPcMUgR/ECQasOGSYUEKXRxKyQ6WUQvkczTplwkrJZ6KYC7XdqkyznJ29",
	"3g6T+KFUjdoUI106lcfbfrfAKTpaVC0dC7mk78SqJsYtrUsdNVGNmtxfztxNSqDCXMbX3GvIq7pxW5HC",
	"QqKCapGiCWIZkRISlBTa0SmAE5ySPzXT1CeqdzfLU5CAvgOi+X8OMVbWL9HF2tOyKugX1ROrSjUJLD31",
	"8wu60vfVejhY0hm+bK7JLISI66zEOWFZmmgHLKZo/WL64p8oYXreqpdqDMP7hEqgahvVIkoXQIhTfgAh",
	"SabTd38wMkj+tL6qmKVq//QkDrVzt3Teq3E5aEXa1bdkTh8ybv+ArziWvd7JCCFJz53YkoKqTK2pfp7g",
	"NEW50gFC0Th4phgZsLwvdAury7QWt3VjDkEXq/Zt49LNdsWsr6qyeVhkU2rErhQvPR/7bIuQOMs7Rklh",
	"d63llndRDpDRHnEpvbVwAkbambkgMfLeTCkvoQgFGax3Gh2zvEixl4duE93RCeBkTx3NPZ9RuXaS3XuD",
	"u2yU5AtsHJJIC3f2xpj65yfjS0yVcKh66oheMq7+/E7ELDe/GoX3fXkQhnYtbPD7vjpbN/SKzQWFIIr0",
	"IjlYInZBhQvImd8VbELnOjIxU0OdR8gQuePcqZ2cgQGpwxmWfnpYe2+E2CihOcyfCS+AZ0/iWlywnxl5",
	"rPCml7Ne+qcHWJMsD9uS9v6CUmVMaQpFmal+XMG8/oITZetyyFNjHijDew3tF1EuJ6Wvtrk////04wd0",
	"zDQlkKoUpLtmvvAcDeqQDOFEoyA7m/alAJZ3O1fbQcQTdUpwSPpdCA1l1FzhpuOd3GTkdmUeD7edAcNv",
	"O17l3mLdc1CfVmiTTrb49U98P76XrbOs+ULGIP+YrDMm64hZJS3DMna8djebtlN1HM7dqZfXE3jKMjKm",
	"491/Gg9v7EavgLmn2ceMnkea0dPQOQp39n3mohnN3vVMRTOs16O+H4u53DH9jkyZZo1h6TIVSOmdM+M1",
	"uX6GS72z20hz8Z8mDFGvKm3erF0A11a2MjYplM7OBUlBmDwfL9wlmUnZ0K5ZK/Vau1tyjAfECAFHCDir",
	"PRQ6EAR6LW8aBlZdOyA4Suv9wjnbdkPjAXDO0/QjoHu0gK6hQTozNENJQXJl03pJqk/0hHAdE9u4IJwP",
	"iI70c1KuxuScahd62aKSUYkJNZHs0NlvEsUoO6eimLvmyk5Bb3G8MlNp9GV89a4HNWWDQM6pjWu5197C",
	"uaH3noraHtJFHrit1aZ3r+yywRmsDYbpBNHNOkNhdKWvrgeK8dV039bnwtxr+4csy4jc8kmBWFdAKyxW",
	"Joag39XX73mHd77vO/669+YT/o3OrxSFPN3+/DMxSF4WnFq9vmAcxThNbVApYfSZdDVMKoYXLep5//MA",
	"rYoM073yGwmNyyGy8WqSzguxpOiI+IS/SnCA7ENPnUNdrDaNARQNrKydRz9hkhYcziM7HxuYJ6LKWIEs",
	"lxsbS9eh+Dr7V3kuB+jEfBwhTjE3cSZMTSKpXWzMEkDzQlEZTFCfrYFzkgDqeIep32veFfHQR505tI/O",
	"o9NCv1Z/Him17q301k9KBSv3ME326l9c2B5Acy/Cv/HvW9Q+wRC+4rAjAXBLmmO/LwYE51VOJeqYeG1O",
	"XZX8menLz41X8QOao16hbp/70UvkLhqNjtjRzh7tbCxmDdEZZmo3G9+std3oPRx5CVSqh18aFcYQzL3b",
	"7KEd6YVdm+fAaLo/UtM9pJRa1vsi/CTKmbsOiy5WTEB54jv5XKitk2z3o2im/z7TK3VlvzsVta9T7NBn",
	"V7ExyxVbLfXA3qQe9lby50v1I7FvJKckBmpucpl8vOggx/EK0Mvp82gSFTyN9iMnKhcXF1Osi6eML2e2",
	"rZi9Ozp8++H07d7L6fPpSmb6ESNJZKq6+5gDtd9/QO+rS9sHx0fRJFq7UyIqqDkNEvuyL8U5ifajf0yf",
	"T19YF4MmkpK62frFzN4UN9ROIfRSkvm9lr7rfYuieq6X0aNEvw+tqlelLtVbj/Hy+XN3/QFM8jnO89Te",
	"Mpv915qMZrd27WV5qLdSMT/+qlb/6vmLGxvLvKcUGOoTxYVc6bzNxHAJXmozxBBWWwnLkDbQKKCLhkpx",
	"VWU55jgDqZPifg9mTpp8RVRWVMf0HwXwjUsiF0UqvYPAZFL6Fz2sOOkeVAc6P9lcBJLNSs/czYZnNgvd",
	"Guc5h7W+NVNP8VeyqWaqJ+SuxFcXXRTQKvegJXWh1GFzB8DGKSUnsawy87Xn3V7IcBnXJjOYcPvo5RS9",
	"gQXWBJEMwRr4przpFJpoWrtxNWi2Z/qFha8kK7LaPQWzHeVE/dsT1c2Is+r+ik7zN2n53eSvNUdkUd97",
	"+EqENJ02LqboaPkKdGqyTbyGBGHhsZMOFXuXPjSFOulFMiJrdPL9Yv94GfSLBdNoL6ghWKXoRdegJpF5",
	"2+Z8vkVV5H1UaYs6en776ug1TpD36uWDUYE5C5lI5v4DwlYPttTgoS4vCy1Efc2SzQ3vnFlWhbEkL+Cy",
	"xS8vbmXUBsjRS06eEMOoQX+8/UENWjhkdJES9/mXJp9eTpq4aPZN6ZfLXvCog4l9PLTrMPejWmULre50",
	"bKjUdvah/jrD3q/ye1A4TA366vYH/cDkT6ygw4AfB2zuIlZnbQfnnABO+vGN+dIKGtnnUbFPruB3m4H0",
	"zSV3JarkoSTMQ7rycOWT3Dj39D269/Sq/98wEtcuc13aw/ze+PXJHNsPQUaKoIrVd9n6alld+SEc0PcL",
	"b+9OREYo/Uhk8q+A3WfuhqR+EjGEyJbWsbIo0rS8em782AvG+52zP4MMXH3doU0+3NaJO+nMYDLPYTQv",
	"jYZ9KrruSavq/cDEAHW36JdXgQ/yM+QmMkrnw5HOKp7fbS01vtfX3246dflMo9U9QkINCQezkgcOHwI3",
	"PRWIOCK2uxMZTzlD+YEalw9yhchw9ZWbruhw6zs4TzhQ3CL5jphxRTvkEa8dPw7SeAwlj6HkRx5Kvk3Q",
	"Ff7i5Bjy3aHMwtFf99pj1cZkiW0NBrc/7ng7qCjwEcm7DRF3TKDTxfXy+b/vduyDVNlmG/2KDB9D1ndr",
	"WIfkbCuMGxLIbiOMvjBuiG0UHOWhW929JONJGuADYGwgAl7RNejNGcxo5q1fugSec2IOluD3N0eWe3Qs",
	"NyAi2EPRWQfQDWm6W+C6BwN97oXj7xNxjS6qe5HwPjBn5n8/envuqa3Y9giHpLaXRVJ+gvoJqYjqs9v3",
	"rCrqE3mqh+QkevXy5V2sMucsBiHwPIW3VBK5uRnxvU5QcLfcBhHl8ODOCCafOJi8DgeGUeUDY8KnjS1H",
	"AfCVtb50fJVooPk8eYcHqSx8osE/e5V7a8Cvg4DviJBl0RjXG+N6T/6KqFFRD/OGqJbUMVrYpf123A/V",
	"1Ouwv13ZbcAV0/cdR/68QUff030H2hyLtpDQ7Jv+/+XMPWpi3+C4CkRqvovShZaa7xPtOvhbGrI10DRs",
	"Liw8mbp/o/VhQ7jG/u8Ac7u3Wh0SD3ijJyO6HNHlmDU2RKeEngscUeAWBdr/sB2S1tLUif0O2Wur3tvT",
	"vL4fsOeoD8oZ3Xo1cfTEDUMUgUSanUx+Ajj567D4h5HFnwiLB3R+f9Ue9g94LuYhIRXX4KHzVqef4EnF",
	"ue/CP7DVM9BfN4e5VCnkXjwaeORnZNW/ovLz3J5DXvhZBNlH1x2s4xY3zTiP5nmfnaw6Jv3dnXj0T+Xt",
	"0q267v1DgHsNTdyZcIxRkBFW3RSs6rIHrpUbuAOBDU+/GgHYIz5hhnJRddY8AEZ6GifOE2VcTzmWn5Qk",
	"V/qKw4nfPOxAaVR5omFe79Od2yO8fBtF3xEhG/QcU/fG4OoYXL3Gu4BOLse46laNtSPFrvZp4lCe3Ylf",
	"4TbwhTfAHWfcNUceDc77Trur8W4H2hkSINrC3Q2QsxmC2mvdPnQbcDuXP0k83QfUBQI5W7jpBHAy8tLI",
	"S8NCO1sYysY+Hg5HPZpITz8eHj3Mdyw3/WM+W9WwbvBXlJvbA8x3KzojQH8C8lqD5vbD2xsaX80Tadqf",
	"bmjcCdKrKk/aFVlReqcz0qsadkbWqD46I0dn5OiMvMY5VUnT6I7cobV2OiS3qC7nkqwpr9vBWN4Qd+6W",
	"bI494p77d0zWuLgL/wzzTW5h9DbwGWbJ1Lp++F6l7Qz/RP1KfdBe0Eu5ha+Mn3LkqpGr3Gk8zF+5hbWs",
	"D+9h8dYj8lr24+bRD3LnEjTEc7lVNVvf5V9Tgm4TW9+1GI1o/olIryoyDhAjXgVPo/1oFl1+vvy/AAAA",
	"//8ExYiHk/sAAA==",
}

// GetSwagger returns the content of the embedded swagger specification file
// or error if failed to decode
func decodeSpec() ([]byte, error) {
	zipped, err := base64.StdEncoding.DecodeString(strings.Join(swaggerSpec, ""))
	if err != nil {
		return nil, fmt.Errorf("error base64 decoding spec: %w", err)
	}
	zr, err := gzip.NewReader(bytes.NewReader(zipped))
	if err != nil {
		return nil, fmt.Errorf("error decompressing spec: %w", err)
	}
	var buf bytes.Buffer
	_, err = buf.ReadFrom(zr)
	if err != nil {
		return nil, fmt.Errorf("error decompressing spec: %w", err)
	}

	return buf.Bytes(), nil
}

var rawSpec = decodeSpecCached()

// a naive cached of a decoded swagger spec
func decodeSpecCached() func() ([]byte, error) {
	data, err := decodeSpec()
	return func() ([]byte, error) {
		return data, err
	}
}

// Constructs a synthetic filesystem for resolving external references when loading openapi specifications.
func PathToRawSpec(pathToFile string) map[string]func() ([]byte, error) {
	res := make(map[string]func() ([]byte, error))
	if len(pathToFile) > 0 {
		res[pathToFile] = rawSpec
	}

	return res
}

// GetSwagger returns the Swagger specification corresponding to the generated code
// in this file. The external references of Swagger specification are resolved.
// The logic of resolving external references is tightly connected to "import-mapping" feature.
// Externally referenced files must be embedded in the corresponding golang packages.
// Urls can be supported but this task was out of the scope.
func GetSwagger() (swagger *openapi3.T, err error) {
	resolvePath := PathToRawSpec("")

	loader := openapi3.NewLoader()
	loader.IsExternalRefsAllowed = true
	loader.ReadFromURIFunc = func(loader *openapi3.Loader, url *url.URL) ([]byte, error) {
		pathToFile := url.String()
		pathToFile = path.Clean(pathToFile)
		getSpec, ok := resolvePath[pathToFile]
		if !ok {
			err1 := fmt.Errorf("path not found: %s", pathToFile)
			return nil, err1
		}
		return getSpec()
	}
	var specData []byte
	specData, err = rawSpec()
	if err != nil {
		return
	}
	swagger, err = loader.LoadFromData(specData)
	if err != nil {
		return
	}
	return
}
