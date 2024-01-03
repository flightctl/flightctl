// Package v1alpha1 provides primitives to interact with the openapi HTTP API.
//
// Code generated by github.com/deepmap/oapi-codegen version v1.15.0 DO NOT EDIT.
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

	"H4sIAAAAAAAC/+w9XXPbtpZ/BcPuTJI7ipTkdnd2/eY66dbT5MZjO/vS5gEijyTckAADgHLVjv/7zgHA",
	"b5AibUt2Ur50YgE4ODjfHyD6VxCKJBUcuFbByV+BCjeQUPPPM8E1ZRzklaY6Mz+lUqQgNQPzF4vwvxGo",
	"ULJUM8GDk+D8LRErojdAwnz5PJgFepdCcBIoLRlfB7ezgCV0DZ7l+PMwCJwmHgD/osnA9ao4VR2CPW0L",
	"BnkO8/V8RmTGOePrGVFapClEMwI6nL/wbHE7CyR8zZiEKDj5DamVH9shX+DwuVgrlv+GUCN6b2HLQs8B",
	"7e9EQipBIdcIJelmp1hIYxKZQcSlzimasv8DqQyEJsDTi3M3RiJYMQ7KHHxrf4OIWJGwBGGq3JkiAPyZ",
	"cmLxnpMrkLiQqI3I4giptwWpiYRQrDn7s4CmiBZmm5hqUJowrkFyGpMtjTOYEcojktAdkYBwScYrEMwU",
	"NScfhATC+EqckI3WqTpZLNZMz7/8t5ozsQhFkmSc6d0CWSjZMtNCqkUEW4gXiq1fUhlumIZQZxIWNGUv",
	"DbIcD6XmSfSDBCUyGYLyCc8Xxj3C/yvjEWHIETvTolpSDH/CQ1++u7omOXxLVUvACltLWiIdGF+BtDNX",
	"UiQGCvAoFYxrK6cxA66JypYJ08ikrxkojWSekzPKudBkCSRLI6ohmpNzTs5oAvEZVXBwSiL11EskmZeW",
	"CWgaUU2Rnv8hYRWcBD8sSqu0cBKz+GhI9AE0NeqbQrhvhdWVK5xZU/gBa+zcpg5X9MjJQAV9h1O3Mp8J",
	"HjHtVcLGhNzsKPyH+wn5IxOrdCshCe3U9pgq/QtQqZdA9TWzZrJFdpx1LSlXBnzntASU8prqX7KEciKB",
	"RnQZA3HzCOMRC6kR9Qg0ZbEidCkyTXA/oosNvTZZAlU+8jxfSgarF8SOm+M742yJ80wNAj/Y5FuoMyK4",
	"cSbXEm3SzzRWMCOf+Bcubvwb2B+a4K93qQFj+VXC3+8zHMJuWrdkvWdKdwkVjlmjFOO/xIrY39XkJg7u",
	"JpiGxCNv79uMKGbut02loAVUSrqb/NHj+CPkovVG47yEZXW3Mn+8unK+rRFu+0NmobQEIGaUcBP+SvLp",
	"8v2AiNQA7EYkR8NnVXDMilZl1Gi5M3PPFNFUrkETNGIeLxUKvmLrbuWw44VY1rVEcPi4Ck5+6+fQ/zJ9",
	"ZqBcSLFlEUgXCvSv+jVbguSgQV1BKEGPWnzOY8bBt6uPzk01LrINT7aVUB1uLqhGC2jEIacFjaw7ofFF",
	"ZYFGn9XlofIdbz04iYFmyIkputWd0pBE/SirGs6j8brtltIOp14draZL1TjKRiZor1QRAhRxFXFr0d5p",
	"SVlsJtJQZzS2Ql2dPiOA4R+jcbwjzEYSzuFvqCJo8Ax3Qw2RGUwop2tIjJUEaSYyTii52bDYry6WzZ6j",
	"nmVSGjg5UuXmI31LGaLulU5fCAXKaa6bhye6Ay7NyoMHFytz53wlBgb05fxSYj9xpoeQ000n6HUUEfzu",
	"9L0qN+46W4+k187slfZihnP7YEwpi9Qiy1hkwqmMs68ZoJBG6BNXu8ZpGiFhxZd6ItsNkNPKDNQzIVHC",
	"l02wLY1fCqHP37Zh/iSEJudvx4BKaLhhHHzQPuRDo+ABVZk0utljYT1WrE0djFjXkukdqQLNddQKVgWH",
	"ihlOQZo0yvLUT/uP+SRiZw0/ZDNkqbK54E2Vsm2MGnT6vEduq0LvPYyqZWFVnfOIZajZ1hj3Dqm0E+oW",
	"sQmynRILGvXAxOGREP1FSgTGK4XKOpgmb1ytsERuVju+j+7vuBRxjHy5tIF3G4fWlHpJ0QXsttKQplJs",
	"aYzWA8yynuLDlENOpca/YamxpU7jqo7t5Q9bgGzBP3U63Q7arbaDR+bykbzGB4rcbEBvwFbjcpOB4e4S",
	"gJN8fsUyLoWIgZroMh891d07nZrKFQLXLAFCNUbH4aa23Q1Vvp1KpueDP+26N/ppl29Utctu1N8/iukS",
	"4vuEBxZALVBzP2mBW8e73HK1vHjJWAlrr6m1v+eHyv/iFfq55MSZzyU40+4lYksKnYgMErWesnf33GEV",
	"8NZ6fzF8KnMfs8y9igH0w1a5W3z2F7y90+q17wESM0Uwx62Ce1kyKKduh7lTafw7LY3746v9FqCnTt2a",
	"u79krWR7y1BJu8HFuw8vgYcigohc/Hp29cPrVyTExSsTLBHF1hzFSpZS7okQ6iXNO3eqEdVhdOxwFh0T",
	"x1VSB1nbMhAdpetFBHs7Cypk9jCowoMWo5ApEFX55OXL6OrrAxq1npqsr1b4M7rfNpbm53qO74LOaGoH",
	"T6n8lMoXK4ymjEvf7ZKHTdkNzGNeGZqypG88SzIS48+MiqF6NmR+niz/o6dAJR8GhQjWxU+5znea65QO",
	"yK/HPTmNsSp78xgFMYRayL1Ho0uIr/LJKG+QpLELshtXPY5yibdpEv2+szGrQLqb1h2uoDI4Lu8xbBh8",
	"gcTMbt4fcVF5ZQbZ0C08wkUSe5hRtmlkyuK/p9WSsTXTl7hx8/eU6o03XJGQik+X7/0XjoyGXMKW5W6u",
	"3/3msForZ3Z/n3Dl3d9+yK6/607ng9N5qax9R9DMHHgr7I6Iuj18iPZenWsh27HrLFBmsZfXici4vuhi",
	"eCdEHFApDYefslwxq2y619jkH/YUJ/CRqW5X/dfm3o9vL/VjVoXqRSr3Y94oBUecu1mCIrm5J3pDNVE7",
	"rjegWVje1iRJpqzFmhHGwziLMMrAuFOZYG1LJROZKqymQUPNyWkZgKDZNCZP8HiXR99/lQ5kRnLEbr1W",
	"TjOe+cpBbsTAX4Kpgrj7epkCaf7GCDlhOr/qxbNkCdLcpEITSCToTHKIbNxZdkMNMYwvMDGS6YQmGMQY",
	"UtEtZTGmR3NyjQGzCcIwxkrp1wyKEHZp8Igw4GVKmQFheqx5w9NFwpU4i1rLb/wBUza61wLRlAy2YM8A",
	"f+i84lNgUtL9zFIFmUTRvyimNHoCAwvRcqFaKpRiuNKRzJ3UXtbNpHWKeO5wQ/kaIiKkJYHeUHRKK7gh",
	"CeMZksswN6VKYeh3bdqUlvV5frFiEEcFtcnNBjjJlA1XmUl0LSctKW9YHCOK9l5baO+r6JLSlpcrJs1d",
	"F5UKjhlcxmNQiuxEZvGREAIrSKnFF+A2tqWcgJR4HJvcduSsCWWc8fW5huQMzYavNducU/SeCzlT2VIh",
	"u3HMiJzD3rDD9nCptMGA1S7Tla+wPz/gnJyvypW5COU3NCPbcUYmWVrnkaGa4aKm9BeY50gpktkE2Eiv",
	"JS+CyVkRwwqTMaNSPCIiYRqjligzaYgCyWjM/jRCU0fUcDdJY9BAngMz8r+EkGYKCDPDJg7aZPwLQhLl",
	"qCGBo6cpA5hJL8rzSHCks3LZPJM9CCY0dz9JniKJODLpEeVk+3r++j9JJAzeCKXcw8o+ZrQc2YiHcJGX",
	"X1L+AUqzxFRV/mF1kP3pIslQxMg/g8SZSb2K1Br3lWAMaRdsLXJ7KKT7A/6goYn9bMQbnASM6//6sRR9",
	"c78RpD+uqwT7LS0ox/BMdX9C45ikaAMU0tjrU6wOONlXZoWzZcaKu7mhBH8jB393hS6laZJ2XNeIYf+s",
	"e98D+UBTo/I2N/8Cu9xDxlnuU0LKq35ByDXlyHSch65nLST++VyFIrW/WkV+URj4oCfqq6NTvQDj5vpK",
	"Uy1ol5AKxbSQnms25Vi9A7FmpraQr5vKUFMDYmpALEptGdeFqKx72FZECbinH+GZNKwnURqA6VPmb6V7",
	"UfLsYVsYpRD5+xj18XozoxhjUzP78VsassGNQcXDkr2XQKOpw/G9djgafO5Rcxzvixmf5SWkSeGn4PG7",
	"Dx6PEAb2q6u/kJ9SpW6EjPxfuuWjNtzL9IbcML0hv1xfX9goJxVS+0OoVPhBPndlSFSWRGh4UQ1IPl2+",
	"R00LYwx5UHJ8sDOFutf1cV4+OhLl/rS4q8vYnDGu1ViJnof2G/sC7tHdwTqwkV5+ZJ/wqj/oZfbDRZ1J",
	"7qwQsi+kcezKKJHgz3Q+wxbVK/WRZkO7I1k4JRtMF14W6UKjCa8bn7KaCj/YyvioTOGUuK9vO7e62ewa",
	"GyANXKn49+BnyuJMwu+Bw8eVWJkqew+QpHrnqqKmqFqXuLJjcUoubcISxlSyFQOFLtAohDtsKCIgywyp",
	"DLY8K7YgJYuAdHwcOyyHKYlHPpoc5oT8HlxlYQhK/R4QIasnPbhdxxz5JeXRy3ou028DPvFUCsQXafmO",
	"a6Z3l64+3z59z2TCVKMbUv0QzhXFtzRmUVueTe/Cc817T0uj4TosFE8XFBMC9zBBzEJwJ7MWNjhNabgB",
	"8mb+KpgFmYyDkyDnzc3NzZya4bmQ64Vbqxbvz8/e/evq3cs381fzjU7MLW/NdIzgPqbA3dtJ5EN5+eH0",
	"4jyYBds85gsybmO7yH1Oz2nKgpPgn/NX89eua28og2xebF8v3I0LS6MYfFfJ7e+VMnvlFafyC3nBzyNz",
	"QRMnl6N5S8bs8ObVq7xNCbZJRNM0NuUGwRf/dgbBWsx99rRw863S8sdf8ew/vnrtEzP0a6ZuHFmppWuF",
	"LLZkCD7fzoK17yK5Sbe7zoypQjmWUkkT0ObBkN9a5o0TkdpyOSkmouP+moHc5c0ZlcW6Ev/ZdmO1geqs",
	"hIGAAEzd3/S6K70/N+lZ3jF85ro7zlSmEramG11vnZm7DsFJYBDKH+0sG8jBrMKfltq0rXneW7OtWZwZ",
	"6rLjZVJmp9p5J8M+4sCk+y50Tt7CihqCaEFgC3KnN4yvuxCNa9cMRmF7be4V/cGSLKn1/yw7CkSrXcmy",
	"43hd9oVN+8y2u7rJX1tO2KrOe/iDKW2BNhq+Js5BK7gEYu5OrRhEhKqKOJmLUCpbgrLNVEOhTnqxhOka",
	"napdr3++8XW9Ph9Qrytv+/Xo9ivPEyo0IpUv4u6s/6nw1eBsU41QZwRaNuDMjBeDzj39JKLdA1PGUqV0",
	"T1pmcNvix+uD7NqIjM2Ro4HExkn/0xX2nAm+iln+5FWTJ7ezprta/IXSezvAa3UyrOqo9lntanewWGGU",
	"ydyCK3TJ3YOqM6fPBB1eke7lIHHSj57npoX+WWR8nAfFcN56s8JodXDmEmg0jC/2VScysWcUe9LMy540",
	"piEM5ZCZ/BSU53HN7PHE4ama9NHy12nMF2Vm3G1AGs9lDTclV0W9fDL0x7Eko1lVsSlPgVt/F8tyBEWH",
	"4uvuvF0xut5QfiDeVXNofUL+DZUfWgTaU4koz0oqh21XJbw0mQoUU4FiKlDcWfP9zzIdsVbhNxb+skVe",
	"Ji/X2G5YbxWj/cLQYfyh5yWj49Y2OhA4bpnDx85e3zmm+NF2FEO955jgy7vLUw+bBzH/ICHSCG/vqZqU",
	"eHvTndGMtB+w8DXIVDJrH7xv+UwsHc3SEZWWAYrqMqQH0tQDcPXJeIhHkahvwDHdR5qHuKRF9V2z/j5K",
	"/rp2qzzgk9BBQUrxNNrfSB3K5+AeWS3qiBzK4M6CH9+8ebBD9F1Q8RzDM/1hlOY+dc/92uKNCcbX16Zw",
	"4MDhwH047I8LnhiT/97RwXE9tHm9ZnyR1b7I1ZEhFoPfSE3V0GBPHbXjwO+Z0sXQVC6dyqVTufTOSl2+",
	"z3jEEmmp+3tuc9lHBf0ZRj52CNflHjM8brmzsulxS5w5O1oeakwp08+qim8aE+nkC556CNvJsoNEFXtc",
	"pqcY6WcK5hyDWOK5wDVxpp8zI2qKXcwxcx9fZR7Vqh5NEJ6oAR8reF2m+17Viz3WY3wCOxmPexiPsVwq",
	"zcj3eV3rKVqTw2t39QmL0QWE6mskHaFaY8r39H2Y7Ds9pkCNs0+VhamyMFUW7qzhjWeTjlheqLzeN6DG",
	"UHvrz1douKxOOIQnq2xw5JJD822jPaHrgzBqfH2ixs8OXzimVNHD8YYT3I0Jmmpgn3qIu5/zB4loGqo5",
	"tI7RwzFEf+LXkfg1orrRyzKz4Clx7fGN+vFF5XtwJHeS41vzv3Mwj3oZYbOvfyyC28+3/x8AAP//CfTG",
	"BXuWAAA=",
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
