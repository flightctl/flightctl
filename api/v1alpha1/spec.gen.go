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

	"H4sIAAAAAAAC/+x97XLctpbgq6D6zpaTTKtl++amblQ1NaUr24k2/lBJcm7NRp4piER3Y8QGGACU3Mmq",
	"al9jX2+fZAsHAAmSAJtsfdrin0Ru4uPg4ODgfOPPScJXOWeEKTnZ+3MikyVZYfhzP88zmmBFOXvNLn/F",
	"An7NBc+JUJTAv0j1Aacp1W1xdlRrotY5mexNpBKULSbX00lKZCJorttO9iav2SUVnK0IU+gSC4rPM4Iu",
	"yHrnEmcFQTmmQk4RZf9NEkVSlBZ6GCQKpuiKTKZueH6uG0yur1u/TP2FnOQkAWCz7MN8svfbn5N/EWQ+",
	"2Zv8ZbfCw65Fwm4AA9fTJgoYXhH9//qyTpcE6S+Iz5FaEoSroSqgHU4CQP854Yz0APFwhRfEg/NI8Eua",
	"EjG5/nT9aQMuFFZFYE/DC/q5WGGGBMEp7FBkbbP24qYT3WkdQVGxOidCD5RwpjBlREh0taTJEmFBYLo1",
	"oqznNFJhYci4PtP7chbXBvFzScQlSdGci47RKVNkobE5ncgSXT1JxuD3VA90DeD9XlBB0snebwbFDjEe",
	"5OUsvbYOhoZDWKz0qEeC5BiwMZ2c6AHNn8cFY+av10JwMZlOPrILxq80IR7wVZ4RRVJvRovR6eTzjh55",
	"5xILDa/UU7Rg8OdsffSAaH2roGp9cmC2PlRwtz55C6mjSp4UqxUW6xi1UzbnG6ldNxIrGA+lRGGaaSak",
	"ySbDUiG5loqsfBJCSmAmaZRWBxNTfRlBoupHOoGBPBL6meBMLTVNviILgVOSBshmMKnU56zmiDbxJo+2",
	"CVBJvUEJrkZAoZYHnM3por3X+ptmP3O60HtVJw9cqKVDUqAb4CGwv7rbx+O3kV76S+gS8HeznLgaLLSz",
	"/8AqWbangZ8RlQgzRDICtytl6Bx+luT3grCEtFeb0RVV+o9+l88REQlhCi8I3FgryuhK09GLNv/UmyBJ",
	"RhLFxSayf4vPSXbiGuuORZIQKU+Xgsglz9JNA/hwXceQdmKxEEGe+4xSMqeMSHPaqVT6GgE86t84OieI",
	"fCZJoYUTyjpwK735qCKrjaffbO31VOP10HSoEIuFwOvw6g6OPh4TyQuRkHecUcXFMKkn1Bn270AvZq7P",
	"GjmhC829j/WapGqjMNoUCZILIvWECCNhf9SXMEaSLhhJUVL1RXPBV4D5g/320czpr0RImLB1zI4O7bfa",
	"/l2a30iKzGKNBENlBZXh3nyuD45B6QydaElBSCSXvMhSzSouidArSfiC0T/K0YAezKWg9Ko08QuGMwSi",
	"7BRhlqIVXiNB9LioYN4I0ETO0DsuzHWzh5ZK5XJvd3dB1ezi73JGud6tVcGoWu9qcUnQ80JxIXdTckmy",
	"XUkXO1gkS6pIogpBdnFOdwBYBsxxtkr/IuzeyhDTuqAsbaPyF8pS4CTItDSgVhhzN+Hx65NT5MY3WDUI",
	"9La8wqXGA2VzIkzLcp8JS3NOmYJ/JBnVjEsW5yuqpKMWjeYZOsCMcaWPX5GnWJF0hg4ZOsArkh1gSe4c",
	"kxp7ckejLIjLFVE4xQpvOuQfAEXviMLA6OxB7eoRPVrmoPaVLeLDmO6t+6g6bZZSvEVayEMXVHSet3QQ",
	"49DNDRk6JhxnRyOnuGNOUd5fdVy+3bQz+lbsdffF9/a6eQWOfOsh+JbeasO1hvEJs/uDGIWTXurb+0+B",
	"85wIhAUvWIowKiQRO4kgGqfo4OR4ilY8JRlJEWfoojgnghFFJKIccIlzOvMkDTm7fDHrBqHJVcjnnAqj",
	"gpKEa3y2gLTdjd2qZBiXOKMpVevS9uDBMZlOjKpphOe/vgzaIshnJXCX0a08ZK0Nbh6ehjVOD4ywMpRF",
	"pDPxaOQitcQKOQyDUKaxnPO8yOCn8zX8un90iMC4IjTmob1euOZpdLUqlNaoJwECEDFh8nRJ0DmW5Ifv",
	"dwhLeEpSdPT6XfX3Lwcnf3nxXEMzQ++cZL4kSN9Js1LEpCQDCR37xNAlpxqO4G/I+VoFtT0QXMX7oN3s",
	"kKWGwAAkURKE6WNYPXCp3wuc0TklKbLWodY0BQ2wuY+Hr+5+kzwYJF6QAKV/hN8B5XoRwHYJXAYXZI1M",
	"L2/11qRHpSzqEn/ththIvHrFYXPle88+efd4afBAUcohHmUM43mlDBejJpzngl/ibDcljOJsd45pVgiC",
	"jPTnlg6L1MBb86oMoF3rWVSLMWtEPlMJZsg6p/P5U/B02gHbCty0whriWpsuEd7nXGmuCuwtgImD8pux",
	"u+ld5f4Zm6FfGL9iKPEaCoL2AW8knaJXhFH9f42eN5hmAFNJe/105RKKyfUnzUvnuMg0B7u+DmjqPol4",
	"SwsSRjlufOHVnhqTpIT7hDOCsD6GytFAUggB4ojSO+3kWE3oTtMPGIKwVKelCfOUxlwcYP5UdEXMTCVo",
	"lfmTpEZI0nBZ2lQcYcbVkoiZTwVaGtqpe3V8uURqHrLRUmvbIWoOihbyHHbwOS+UhbjbOuucAz8RRsy1",
	"HV79zAk2s0XZ0jCaOjausARuqC+xFBW5mda/53/4PnjPC4JlaPJvzgUl82+R+V7JEW7GZ7LXOntqim5U",
	"pxm6kXp2CxqrreHUQjANEVy5/Gr3O49KxTOdNftUFHqYNziTZLD9ujGuHavxqxu68bNveq7jwYPOcSJj",
	"w3Z/Gq4EUFuWtA/GT2ounto/3Pk9wkJC05M1S+CPD5dEZDjPKVs4Q6rG8q9a8tSY0KqHdRTlJHE/vysy",
	"RfOMfLhixGvfD1+vmeBZtiJM2TvMW1T0nuvTpsRItEWJqmOSc0kVF+sgnjR6oh9ayPQ/loh9kxGiItiF",
	"bw6Xr8glTYiHaPODj27zSwvpp2SV6yvSqlF2DzQlFVLx1e3bdqdN9nJipDjrytLcZWXaa3aaABSlfCxn",
	"bVleA2sW12Zd5ve6GThfriVNcIZS+DgbDTijqXc09crdimX0v61tny2MuKHL1YxWc7FG/OgOAzGLxKCg",
	"ibZh4h3O9VENeNoNWoJ8aDqRxiG8taO9hUFn7rbjxnFmvM0xbAnCUiJIGuVqjqVZGT51XNN089zVmzTR",
	"+jyd8EqekTaoi+Ojg9f2qAaVcqnvU84OXwW+NsCpjeX3jMP1M+cX0l1yjVthrog4JuecwxXbVg1018o7",
	"C82RcO0RYaAx2PsMJ1ZH1CxQS+BWnL+iaolAWbHEJ88YF2AjoPr2Q6dLIknZnSdJIexU3sYtsbQzg8aZ",
	"ZfxKg6Cv1pxLtWO+IYXlhZydsb5mcoMigwK9WscqmnYSgKeURfohqrDN7x5PhpidgTRZYrYgEi3xJUHn",
	"hLCmfm+FhKFYguWTLiydkzkXpD9BmfYeRcG+wqbeBbLsdB5V0Yqo7oBozHy9qcaCV5LNvSAjTDpYkHsi",
	"muso3zqEFVIVDTzreTUFR7N3VDsEbOO1FBno5mFxxrpShsRRN8/t2CC6gB8aDLdxLD+kEktZ18arGMSP",
	"TBZ5zkX/6MngzOUUwa/lvMGvFTCRzx6E5crDjvfqW93Lbn6Xo0720E51byMGMLDRX/7Y/OXTYZw/yuu3",
	"drSbcT+chIVqugqa2blUghAEX23wv0Afj99uVkHMgJ2AxEKzw6A0VKMPJwaq4O0CX17RRdSvnMK35ljo",
	"GzJbzJBc4pd/+2EPP5/NZt/2XGh9zviyG/JXW7lJIo4vDbWThRS+IMzJQpq/GYHaaslGNjTikHNpzNBr",
	"nCztAPq4+8GrGgVcpEZ1WUM/w77T3lxHL2g/MR6xDcEGAVXS+XI3hEYncceZQ6413UYoK8mLvlKyP5CR",
	"NKaTlMqLm/RfkRXve/5DIzR9iXkxKQe10PXFTTwB4J9Y2ASNA0EVTXC2dSpAaGI/06D9tZo89NUDKPTZ",
	"ARn65ntmPPNa+/h5FqH4ney36n1EmulogXOSRFIV3Lzme90GX4kPVHdZUYZtrL1d2doEqNjBHS32i/T/",
	"iSpj0HL5ZaXnoKvXL2WgzQlJBFGDOh+yjDKyxaw/K5WHuoWORADxNgutTRIrrJLlEVZaqqwHWeXmx8ne",
	"5D9/wzt/fNL/eb7z485/zT599y+ha2mzErnUynU/DlFZyPR29uxkr3+TNmdl0rYYr+GzaXNG3LN+oLre",
	"3Z/0G+6n0A6Yuysdgv4V/vyWsIVaTvZe/u2HaXM79nf+1/OdH/fOznb+a3Z2dnb23ZabEtf1Y9E6/lff",
	"4xXWm6vIHezMFcj21VK0EphmJlUxUQXOqngO3OE3q3OxzXQRMPX3D8Qpl2gkCRA5sLXaaDCD0Sg+9P1i",
	"g6uYmy7OuXmtNRO9Fhad9ryVNUKPkGGpTggB4aZfXMuA81rOUjuxQyWIwQpIwx/iTuihNRD1GKBqfz2d",
	"WC1uiPktjfhiPKqsQTWt072PMH+TS2KBXaggq/DjbWhcnrqH/FJr7nXhU7dnULtRUmlsCE+a/AB3eDib",
	"tLKzTydH/IoIkn6Yz7eULWtQeLO2vnmABL7WJcfaJx/cwOfaCgLfA3Jn7RgFL46yhbXLmIBamsrdoqAp",
	"2LsKRn8vSLZGNCVM0fnat3a37wPP2BHWLPe9Fpqfg/HQBcdWw7aoTiPHeAAbuZScK3T4ashQGmBwIZj1",
	"h+H84BqhE6fs9pygqUz6KCnX0YYifgIaPoItNXkOyjy6WhJWBq+bcPA5zQiy4Lgo1i9anddKxxua9c+E",
	"1Y0/OASEAMmxWobxq79o5DrBFfxR1k1EWcN/pDEN/iYqTccEM2TNlBwRCj4q7LYmsTsjIMmaKarxSwWE",
	"g617EN5GK0b99rt1F429Vcy1d5u3Sg3u7W6V9hDerfIxP+WvTK7Mh0J9mNu/vVi7ba6Q2pTeFIGv/qzB",
	"zo2gv/rX1k3ge+EaChiyokg9DkS60z3PCFFIEFUIRlLDPOZEJUtwwCJJ2SIjCOISO5WDisRi4Ts9gpW9",
	"6Pdpax3nguCLlF+xzpWcr9GZD9fZxAo9XcE9jwp4C1M34IornIX5FXzyyuKEZuoZPG4O9qPCjhWxu7DT",
	"jBMHVE0DxNrc/8aCg7yFyouHDp9Nqbww+VDtExm/xsp7JXih1cfsvnZgjk/hkF0qRQGz7mcZv8LB4hiB",
	"RvUSGeSSZKDX688k1cDZDoY/CZ5l+h6iQCC54AtBZMC/vBC8yP+xjhtBM3xOMnRB1iA95URoQkbQzcVW",
	"ATVW82MH8bAssxX+/JHhS0wzyP4KbpCtfeKdXId0VPYsD4YrYmYwES43taJsf8OU+HNjyoK15yq3YeOc",
	"QftW0ZX/4iAok1vdZGVRLSOXKo4SW59phs4YELTrYr36577EiyEonGtx5JIgCyA6Y3Nuxz9fI2zSjgpG",
	"1QyduDCH6keQk/fO2A56Jp8BQNJk6cJPK/PTirJCEfPT0vy05IUwP6TmhxSvJYQN+WbFFzs/fjo7S7/7",
	"Ta6W6aegObHKh6gKDzWL57kWOzbYaZN8VY15YjtcTycLkSc7K8zwAur87JB4sGaDFwQA6BguxFFbSR9t",
	"Qmk16SgBY3MaQdqGbp22zTH+ZMwJeHI5Aa3jNCw9oN39dsu9RLLAjLjb0j9M7leL5twXl8VJpBYdQPv2",
	"EnwhqNoFCEN771Y75zwjGIwF7uu+is+0D/KIHhwuEKxsEUp/uissazP1M/C7HiFJpvrmZm+U1dRfRVAf",
	"B+HnJnVXzQA1w6L9SXHwa68bobMbRfVyP3vRRTgKMdisHpDYajJeDQ8dmhjckl6Wvbb8MMYrfqX1fcIX",
	"12YOoJuZffYaGsdxq+0ziRQWC2Ldy23OkEjRnjKRwkwQqirjVyOUJuu4rDARQnDaiAjon6t3C0x9v8nK",
	"XS0CK96jK6pl6oq7U+nMwKCba2qulAJASpWg3c39NWb7bXskWCLScFjcRK/LoRJIBrGmUpK5nnZXRPFJ",
	"pkVX7Rops8GlT9oFPcgNeHBHPMWwoiVt7bQt8xVqqZlVUloVBqm7+4WCiqie4lrQLoV3OtlWsy4V7EBd",
	"Xm8F1QRRqHqhClbWjlOFi2bHI5Ydx7zbFGPaXpB1rE1zNyODt4fqtYLonvsTaOxxQdU6vg5TfakH+PFh",
	"y0GCgIOPvx1eFiswA+1dXZmN5tWyUsn1dFJ3W4bN/escTnDp3jUsW6saZYV6bs3oNANW4bxgB1DLCgIp",
	"VvyydICRMrSip/erBmU5aO3Xcobar+V0jbZmbrv+sEtcyzaERULy8wxThhT5rNA3H0/f7Pz9W8RFswCc",
	"HcFxP4ecEB/V7V7rbpEsxitXO0cZk5TQ0h7MMkPvCgmynPX9nk0AuLOJhuhsYmA6m8zQK+MgATm/bOTv",
	"Fvw0mdou7a0BOx4v8jBK9PKeSWPbnnqGUueS1peMS8pgxYoImqDDV02wBOfKQNUWC3lK4lP/v//zfyXK",
	"iVhRyNeGwooz9B+8AHHZgGOiLlZauJ3jFc0oFognCmcmvxOjjGC9A+gPIrjJr5ii5z98/z3sLpZnTAt4",
	"CV3ZHvp2D3f6/uXzb7XArgqa7kqiFvp/iiYXa3Ru7b6ozHubocM50gJ5ibTpGdOQNpYD9kfw/6PUQ5oG",
	"0CSNti30cW8NPpc8K1QVfeBI1J1lF975nitiTnxZfQ1cF7opiGrnBPFLIq4EVYqEPfOFJKKTavgVFBq8",
	"daoJOZbKAxdkveCIbsP6xnqxPauwFWPTMflwNP6Oxt8qEEqflGEGX9Pldo28MGbYgFd+qhvt4OfxHD+4",
	"pa7ah36Bd8CwR5PcV2qSg+09NhEB0VRJY2wo35npEzVQsakwf+gw6UGw0EYzno1iOOIZTTamMRzXGt/k",
	"HRplK/OFtMf7qAHWjKIM8+dmBJUDOkoBMYuc93GYFc5EqfVNXoLWU0RAQMVZtka0inurWpiCQPogQ5Je",
	"4uo4V6EKpZUTqnxfLa1O2FI9hxnWypC7mycnpa1wzyH5/1NH9r24dv1YD7TkQeFbmtgEKXeQB+WVtrDu",
	"vm2f9+0NYrt0wH5Mcl4G9wWt6XOcSdIEtE9lWze0W2ohIsGc3+QcSo1qcWHFFfkWshRMgdJej4DpkW2b",
	"4FKDWbm9oxnbu9x+3XJB1bEeocXweMHUUakB2/DQye6k6ZI4siqwTVmmzB7t0E3oNOrA044ObZtf2vRQ",
	"XIkYHBWSaI0XWNWaJch8OWPBTFC4fI7JJZXh1IZWlb0SvFbnaSzislkazyA6HJnppWHUHmdrPkhBEluh",
	"vndax+uyT/CK8Yb81CYOL1e332wmlyYN32Z2sPC7pSGIO5+jbegSDPHcsIBSJ/nl9X/826/7bz++No/M",
	"aiKRRGkiIYE3aWUZ1FjhZFgYqSgiRmAtYGq9ov6a3BRRlmQFmL8wWyMsFsUKLuBC6t+kwizFIkVySbJM",
	"E7XCn23yinnswhrBJFrZEsNuJolymkNttAVE1Uz1ouncpAldEeE9aVewFHJezrFcop3EmEk/h12fV1xc",
	"vKJiUwQzZV5wTYXM0uAlCmaEfDpHFPTIjMwVIqtcrfUP0K5s5B54kGjJV4MScPR+9CW1YYzVI/heBQtC",
	"tN0492E7uqIrYgWCMTp3QHTudee2+1zqJnte3yu97MGc8qPu1JIK9I+bLgp/gL3t3qy2HBk2DHH/1FbE",
	"4OUluvNrI/G1mg3MqKIhc+BxomrTwPBzmpEpkkWyBAb8GWuCnFmBHoz4ZXgclaAFVE+3lF8cBLhQHGlx",
	"lV9CtdySUYBhXd/HXYmn0VzNMu/PIcZbvJeBwJsJnHAK/KvCOYVeM/uczCsq7V/wZDL8n+emBr394Zhk",
	"HEPaMiYrzuw/+7n4LC2U09l/e7NaineTu38CDPZfFSjlDxYiN1wNsMAF+IXdD/YZJo8qgrdFWW1moKaR",
	"4FkiVOj1WUl++N65IJHgXJnXTwPispRXXKSxzFfz1UTWF2ppHHE/n54emWRPzZP9MNZyuFD65wXNjT3u",
	"VyLK3Kb2xCcXNLfKjntG6dLvEIrPVZnshYnTtycQNoOsXasX4HrwC7LuP7hu3HdsfkFifn396VYwH3/i",
	"6tRSNrC+DVP1uf/CZZNuVZtcKpUH1UnNmI+6k7g9Tz+6WhJbCVkQmXMm4VaQiosq8x2cuaY2QC0vcRbW",
	"+e5ZxZTFfE4/t6c6wqIMavh4/NY+W8ZXRHpFxc+xhK8zdKggR91oCgT9XhBIERR4RRS4O8yFunfGdjUS",
	"dxXfdWbzf4fG/waNQzB26bjldm1Ua92OR8QV+LqVoWZZ47v96oH1fbqot4EHzhlsE0cJzjLEBUoyzszD",
	"1UPMO1N/QaF75nCFF351JndIe5fNPCZzIuBFc+vhKmud2ZqXpVrgPa6Q4+SiT9hTvMhntIzbrTIWakq+",
	"DCkoES3oXVuXGTdMvZ3l7W51eRLG32yI61+BA2TDHCc9zK5WDqp6TL1JNzKACvQwEutOlkAFhJV5+eOC",
	"rKfGnWitNRA1Iwjaf/8K6qBo8W+XFVlmE4Odl0ciqByndYYlZYu2RwA+vx0eet29bn/U0LkovXlBX63+",
	"Yp1j50Qi514yq5ZrppZE0aQqAIlWhTQeEt9slFGpTL39SywoL2TppQEw5Azte6X98Nq4WDjL1vBcH5+j",
	"PyuH1RQ5wK6DXhVFWREKhrZfYPxzAiY26r3LCSY3lNGV0TFBlysTmuEwl/Ut7GOp3oOqXnQ7EZAPBgFo",
	"gKoyFRoeELCuaCoRz/HvBSkd+ecABxjf4BVL9zRhmfZluaXnbcbG0wSap9Y+qGkliBKUXBq5hJHPykUx",
	"VUnZJd4PDFZMmY6EM0klhDPCWBos67C2DgTiUGZXWi9bo9dtatqkCIoNgGyEGcJoTq6cGcVsbg7V5g1K",
	"3Na7KAtjNaxXEzG2RlhnuZMGlU4dM4WnEpO1qypMOylMmMd0QUqbooJlREq05oWBR5CE0BKVVmzWehtm",
	"iPiRt7Ow0LbClFG2OFRkdaCZUpsA223KZLuSzmRxLvV2629AchZ62A6jjGpWozfFilpWzHTb7xZYWirs",
	"r4aE3E2bWtYEoYZgoXU8aqo7Nam/hNwBJVFhiscA9Rr06mHcVoAeXDA4UixFfEWVqlL/JREUZ/QPIJo6",
	"oLC7xgSIvrHhgeckwVqiNSo2+F2XBbvQI/HqK6DA4hOqCkGjb6v1CGJRZ+iyuSazkNJkvdVKXKAIz0yt",
	"K8zQ5YvZi7+hlJvQT6K8OQztU6YI09uoF1GK9SFK+Y5IRVdQ0Oc7cwbpH9ZznfBM7x8AcQABKKW1S88r",
	"CDDS2NjG3A88QpS2f5z0K+8S0uDeQQXo2y9woq9pL3ChdcKqbxpf9btKC8W55i/w7HPwvjLny54rKFjh",
	"+KQ13EBb83RzIIKMMa4qq92WiVNVY/NQ69rPmgrW8HFPQ5/SFZEKr/L+NT9TkpEtuy46XqTdR4aHJSUP",
	"qQVeeXXLvNdqS9VYQq0SE2+DjprPYhtFeoaOCU53tIDQswbRjTPa3LtvJp4Mir0YeSYrnASgFWDvFudi",
	"gZk+ovDsNVZkwYX+5zcy4bn51bDdb8vrOLS/YZuLbwWwbUOG5CtGgrKsF/OGFeJX8B43hC6a37Xwhs4g",
	"hmtXT3U2QQbJkduvdn9HvJ4g7Vj8wbS2UiR1r+QD93wmvVDH6oGFKoKynxHpSEu9XimQ6jnt/po9jyRB",
	"eDkypbHdT7HAaQq1XvPMKCnCZK186gg0aO7P/zz58B4dccBE3E8AxBeG0cg+iiOcgixmoZm11AOwrEci",
	"A9qW8yMiEsJUUK+vvrl72G62oZw6E8irxqZV7Rz/5zcvnj//3+A++/ffnu/8+Onb/xEsbXNsXxVsVrLv",
	"fc14HV9bl31b6Y0HBTXx9WfPKvZRG8p1OOjArXPIQwE966CHEdhZ0DqU2+SebOxV7Boab13k/pEXsW+9",
	"pxlll19uofttStYPfQ20ZvsMGBGrr2XFFJvDWLeMe4x5QZW1bwaZ8XGH5f3Yt7R7+UE/UeVb4U0NVrDG",
	"kup50THVYEwZevIpQ9UJGpY35PW73eShauBwBlH9ez2NqPxGx6TAh08mEo3d6Hkzltx+zCv6SvOKGjxn",
	"r6983gzd7/PSU+/GJ3JZtd0AdSQhptliWFZMJa/0To3xutw8kaU+2P2WhnHy8H5GhDouQrHbjccEmqr5",
	"slhhtlPWtW8kjgH69NjhmkzRKrauvm2t+h+/JMILQcOXRGiFGQosgwfJVeZwDy/qibUujd4ACey1o2P9",
	"2NhGxOu0Ge86rUe7zurBrWdn6b/+JlfLcNHZvMNQcGqqHjj9n8/tiowbTdDFgggZxKQxJ5rUuUvS54Wk",
	"2n6f2E7hpwDciN421dZRtwhuJK7aZF6sZfA5Q3h9pV8MZXSSauBoE2/GaBsDircapzqGMq9WOM9tNZOD",
	"o4/R03v0MWTPN3XQo5p1pEa6cy/E+sWdD1UymMsUs8r1sCcII6vZxPa74NpgY4hg4jqwSxHbkON2XSYH",
	"aIREAU+PfHC+d/NrDg5yQyQgABkuMtgMUbHdUHlzbzeCBZnwKs8oWxxq6fUy9HBByUXPiboihJXWE+iq",
	"13UPjLEW9R8J+q8FaHjLnvpbFVhxF9c5WbMkJCpUX5sFr704LAizsC57U30Gclc904biJpgQAgysZAsa",
	"TPny2agEjWaO0czhnbehhg6v522bOqqhnbFjPK0Pa7KwfdcsGXyLAqcfjRZfrdGiwUFahzXfmKCAyyfh",
	"aulMDe0bHcKrt66FLUhX9ajOqMKUmXjM0N1vcj0YP2OyOHfdqT6B8CgggNIYy8R6uBGg8iRIIGfMRme5",
	"B8UfRZJEOw8/kDdmI1eEbdXG97DUhr7p+w2CiVqMmm2G2owqfnUzCxDejvd11jNxhpADvlrRSBayCQqE",
	"BmiJ5bIqcKrhIGl4593IP3XEO5Wje+FMocH7xNINMWWZwio2JoDYAMA+BVKkEliRxbq/zgtVl05sVBdY",
	"LRt5AW7EjUH8ZcuOJVXllBpE7H92ljL3jFdufm0Wy2na9qBAiKkYe1olrXeq30X13mfaRnaPik/NLdID",
	"hZ8422AGaHWBBK4kIVKeLgWRS55tLILhhfAEYyVO5HKrNNJc0EusyC9kfYSlzJcCSxJPCDXfjZYtl0dl",
	"38eQB1oHaFPCpl03Ojn5uX/OZgTxW6agSX/LNrge7igBTa++EQvh0tG2TEOrFhXiFLF7zt5t1Bg4VCGY",
	"FXfhvV2cubcIUs6euQdLkcmz8IIwe1bk7uMMqC5RI1G72MFIICWWYa/DCidLykh0qqvlujGBfddQw3A2",
	"eYNpVghSvXdpou6prNJRTNq6CZSHOPu6VFAlseyjYwATJRkWJnzTxbzYxeqDgc4LjWViIvb5JRGCpgRR",
	"teFV3+B2ukDXEnnoA6QF7aGzyYnhf64UdrnSO1cgtLa9g1m6I927nz0O+aktgvfKv6JrtRjCtbQ2JPl1",
	"pDdGE6r7uRuCAJcwTiIrqgEba+SDHGvj5cx+8tAXtVU0GtQtnn48MXLlCMfwjdFyOVousdxtHJ1hxstm",
	"59u1XzZGD8drBRrVg7YaDcbArQe3goZ2pJc1oHkPjMbQr9QYGmJK7aIt4eccTss336+WXJLyxnfncw5h",
	"JnxzASYzfh/wqjfue6Wf+DWVpxv42TZWu3LFlkvdQvBW9Qjnzc12ltbNe6h98g6HGMg+XevmGkd69Iwm",
	"hBmDhEnnmeznOFkS9HL2fGL12ok7WVdXVzMMn2dcLHZtX7n79vDg9fuT1zsvZ89nS7WCB9gUVZke7kNO",
	"GDL7id5VlaD3jw4n08mlu1QmBTOXR2rrvDGc08ne5K+z57MX1sYLONWHdPfyxS4u1HK3Sr1ZhOj8J6JM",
	"yaVajohfMeww1Qsu1LIUtl2eOEz28vnzxmNIXjLR7n9bldRs6aYN92aBDWhk6P6i1/39i78H7tcCfAiq",
	"XIXGEQxRw8UlzmhqS58HsfGrbWBQYkpjhVDh2gHWXZ0iOLFUD7MkOCXC1Xc2XerPrZXoaBLppzB6G6cb",
	"KgzAagAlz1/E2lBWtdoOcd4LZfblSHf5mNEyEno90Pxeyy7XTOCgGuzEDObSLJtYfgUDRNvLuyTDUgCN",
	"kaDB963MZR5yC0z1kdn34P6ALZlOFF7IxpNx9Q0BJTdI1iDEduKyjnx9FXc2bxB9vExx2VDLoqawl/PR",
	"weNMpbRj7Kt+lRN7Z8AIegBIoDdVcFSz0TNX1uOZLcFgjVe5IJdQMqZe30JfQBpSAKg6pmX9l64DOg1l",
	"rJsCGDa8SQmaqKosBTjsbTUSVxLAJKRTYZ84rb9WRS6JWJdlfkKAZrVyQ4Og9Wvq+kU6zHaUgPqlQ6qy",
	"IKdV8RaocWFqUsTRX+uO6Ly+9/BSmBm0UZUFIsqXhLVK9lbkBBFmXsUTwFAUX3QF2XsVnnx32l9fhtxp",
	"n+6QwUTPFiinHXzn+d3znX/gFHnPWD9mXpdzGSyVY+rVeEhGFsstRmdeiuy6lexo/+Dp+u633+CmklKV",
	"KMj1Q9BhnAZf3iI9DJrebFVqYHj5MDDsJwnJSyD+fnsHo/2AfGDyTBCcriF9UFggRo7gc4ReUuvun/pS",
	"uO4lvAZYCNpSYN0kNPlBR93TwgVn36m095stblhnHFtoGQ/FVB6ApPSk39/9pO+5esMLdmMJXh/9RkH2",
	"pLcudUxwujVhVnabquaOCFBqa9Sb0+l0UjD6e0EOjbEIbsORdB8x6ebufb36SDkWyryJZox2DULubxSA",
	"wky3wmLj67hFBttXctwBvP3rsH2rFam6toLjKCf6cuITkY7unR/oCX+8+wkPOJtn1FYA6smAiuDdCeXL",
	"tuY6x6b/bYt2d3BhDuQ7o8Y6cqKRE90FJxqiie7iPBe8TEmOqaRsvTUDe0XY+gvgXqO4/1QPVdSWa47G",
	"9lf3vun/5Vzdj4nSxyvrCz5dxoddnbHp5G/3saGHNsbQBLeVzbz70D4fvkXwgH1KPGJ5rb4+0bgA+yJ1",
	"dxBADIdvqVTVt9G9/6W69/fRnGZ2P4Kw2uPnSuLX0Gy62gL6hUQXZD0UdNPzDQxUg7x/+d8xYmHLiIXb",
	"JV0o/z90+82bAQMp1mZPonmGF/CCkX0HEEoGaJStVlis6zGkcob+qdEN+8kRyFb1pxRhu2vVB4AP2cG8",
	"8FdbrwqoAuB/BpHxz2qn8Jn/HiEWxD1L7l4YemYH1kM9g+xiUUQZkdc2hKsym/RORULD7ceAEyuS/fVe",
	"ZEBX+yx2cYe1IPNqDML29o5EsZQf78IAaAfvZe17cSezjra1B9EbQnTaluaHBFVEiNiX4oeo5WWPx66D",
	"x4n5SXqSN6krgYiHCOUcE5z2oxtjX0Qj+XxV5BOJOgAHuXseqqShNExD0Hg480lvnXq+mpiBzfQ62he/",
	"Ivti5Gj298dHmTs0fgxywcNK1fd3MkcJfmQF96Yy7HrvvwXlQLtn9oVpnoHpjdliaG1uAY3dM3FfvThY",
	"voc3+qsfOZm7V+uidL6wlul5kWXlI7cmP3/ORT8p9ieiAq89bjgF7+9Knp1Ga12a57+bD/mFjaXQ9rjV",
	"9GFOXQC7Hdfo9+1dfs+RA2Q8nY/ndFZ1iuK2CFkrJzfAKnHiSryNNq0nZJTo0nwGk5KnAz0GanoqmtCo",
	"mNzfkfGYMynTYU1VEs+7EK1hY1qCqGS6U7ZwvuLWgarybcuaNhtz4NyJsiGJKTo4Of4COHRrqSOx3xex",
	"oza1Nyk7Rvc3KLFTbXgsUq6Vbf6Eg+ZaKN8QP1fhDnVWzwnieAyrG6vmjFVzbq9Kxhi81IeZdVfJqfqY",
	"qo+dIUbtOiV3ow1E6qHcX+BRr4IstYo0YzGYpxMIFTpnnWLckPCotoTRV4wbYhMIzvLl6DJjnt7WYmwg",
	"rqrCa9CKOZjQTC4BWxCRC2ouljrNjST3tZLcgICPHozOGj5vidN9EZUWthR9HoTiH1LiGq1VX6u7blvp",
	"qlZHoTuRwjZsO2BCzCKYUf6kWdK+Q/RDs6Y6IKNR+17ZxMuX97HKXPCESInPM/KaKarWD5zKfgt86ibB",
	"BpsZVFBiH+40HoX1Jy6s34QCw1L7IyPCpy27jwfAZ9bwSNM23tY3pmPYQld+fKLOVfv0VadDNYLAt1Sq",
	"8tPoNx39pmPtjkdYu8NV6oD4qHJ7XYkZyhDBydI8oxeZFKc2SFYe8IKphyuHAcxmdCjHGPiGwhRv7BaH",
	"nMbu211IXGbse3YOe5OO5smHthY6Em0Jc7t/wv+vd907lvYdxW2kvOZTmDGBr/kk7SbZRV9GwHadZNGa",
	"aBbWeObemXp4vftxS6GN/d8gj27ean1JPOKNno4C8iggj4GFQ3hK6IX4UQrsYKD9L9shkU9Nntjvkr0x",
	"6707zuubMnvO+qjs6a2H8kdj4jCJIhBrtZHIjwlOvxwSfz+S+BMh8QDP78/aw/YBz0o+xCv0xrcnPWLa",
	"itoJxtIc9/E+ywbvQ4A3h6lUM+ReNBooJ3ObpBq1vsZKJDuxv5/99cSM8cAW2JEBt02vQ8obzoMkDG0H",
	"89n5bfPZr6a24UZSHYPOvs7YVO9U9g90j10r0PbhpZ8H9crc25kcHUAjD7gtiTKmCt0osnOD8Dk8eG5U",
	"k75wuW+b6MzNd80jIKSnceM8UcL1mKMgOZdUcUG3eu/t2O8eth01mjxRD3eJ5/UG57bowuhbKlUDn2Pg",
	"5ehXHv3KN6gW687l6FLu5Fgbogu91uEQw2O/wV3IF94E9xxs2Jx5VDgf2gZUo92ItDPEN9ZB3Q0hZz1E",
	"aq8N+9h1wG4qf5LydB+hLuDD6qCmY4LTkZZGWhrmUeogKOtyeTwU9dU4mPrR8Ghh/toszM2D2t/J1Mn3",
	"ocOXeFDvTkK/37M6agQjg7h9BlFTPiQvRELkmiXb2VpN/5M1S6JqSNXkSRtbK0xvNLd6TcPm1hrWR3Pr",
	"aG59annup8t6xGTF4PSmzWmmwXJrO4/CUhNTHkrvrA7zaO/dwDQ3Wnw7OKez+dZ4593IlN4U9273bc49",
	"ynkPb/mtUXFM/Bpm/O0g9LbcNUxzqw39+M123QT/RA13fYTNoBm4g66MIXikqpGq3G08zCDcQVrWSPq4",
	"aOsrMgv3o+bR7vP12X2aR3aIabjzLrDG4S/zyN6lMH/f53ZUH0Z2cTfsQn8yFiZznguRTfYmu5PrT9f/",
	"PwAA//9HCwOOO4cBAA==",
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
