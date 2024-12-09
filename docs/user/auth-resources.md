# Authentication resources

The table below contains the routes, names, resource names, and verbs for flightctl API endpoints:

|Route| Name| Resource| Verb |
|-----|-----|---------|------|
|DELETE /api/v1/certificatesigningrequests|DeleteCertificateSigningRequests|certificatessigningrequests|deletecollection|
|GET /api/v1/certificatesigningrequests|ListCertificateSigningRequests|certificatessigningrequests|list|
|POST /api/v1/certificatesigningrequests|CreateCertificateSigningRequest|certificatessigningrequests|create|
|DELETE /api/v1/certificatesigningrequests/{name}|DeleteCertificateSigningRequest|certificatessigningrequests|delete|
|GET /api/v1/certificatesigningrequests/{name}|ReadCertificateSigningRequest|certificatessigningrequests|read|
|PATCH /api/v1/certificatesigningrequests/{name}|PatchCertificateSigningRequest|certificatessigningrequests|patch|
|PUT /api/v1/certificatesigningrequests/{name}|ReplaceCertificateSigningRequest|certificatessigningrequests|replace|
|DELETE /api/v1/certificatesigningrequests/{name}/approval|DenyCertificateSigningRequest|certificatessigningrequests|deny|
|POST /api/v1/devices|CreateDevice|devices|create|
|GET /api/v1/devices|ListDevices|devices|list|
|DELETE /api/v1/devices|DeleteDevices|devices|deletecollection|
|GET /api/v1/devices/{name}|ReadDevice|devices|read|
|PUT /api/v1/devices/{name}|ReplaceDevice|devices|replace|
|DELETE /api/v1/devices/{name}|DeleteDevice|devices|delete|
|GET /api/v1/devices/{name}/status|ReadDeviceStatus|devices|readstatus|
|PUT /api/v1/devices/{name}/status|ReplaceDeviceStatus|devices|replacestatus|
|GET /api/v1/devices/{name}/rendered|GetRenderedDeviceSpec|devices|getrenderedspec|
|PUT /api/v1/devices/{name}/decommission|DecommissionDevice|devices|decomission|
|POST /api/v1/enrollmentrequests|CreateEnrollmentRequest|enrollmentrequests|create|
|GET /api/v1/enrollmentrequests|ListEnrollmentRequests|enrollmentrequests|list|
|DELETE /api/v1/enrollmentrequests|DeleteEnrollmentRequests|enrollmentrequests|deletecollection|
|GET /api/v1/enrollmentrequests/{name}|ReadEnrollmentRequest|enrollmentrequests|read|
|PUT /api/v1/enrollmentrequests/{name}|ReplaceEnrollmentRequest|enrollmentrequests|replace|
|DELETE /api/v1/enrollmentrequests/{name}|DeleteEnrollmentRequest|enrollmentrequests|delete|
|GET /api/v1/enrollmentrequests/{name}/status|ReadEnrollmentRequestStatus|enrollmentrequests|readstatus|
|POST /api/v1/enrollmentrequests/{name}/approval|ApproveEnrollmentRequest|enrollmentrequests|approve|
|PUT /api/v1/enrollmentrequests/{name}/status|ReplaceEnrollmentRequestStatus|enrollmentrequests|replacestatus|
|POST /api/v1/fleets|CreateFleet|fleets|create|
|GET /api/v1/fleets|ListFleets|fleets|list|
|DELETE /api/v1/fleets|DeleteFleets|fleets|deletecollection|
|GET /api/v1/fleets/{name}|ReadFleet|fleets|read|
|PUT /api/v1/fleets/{name}|ReplaceFleet|fleets|replace|
|DELETE /api/v1/fleets/{name}|DeleteFleet|fleets|delete|
|GET /api/v1/fleets/{name}/status|ReadFleetStatus|fleets|readstatus|
|PUT /api/v1/fleets/{name}/status|ReplaceFleetStatus|fleets|replacestatus|
|POST /api/v1/repositories|CreateRepository|repositories|create|
|GET /api/v1/repositories|ListRepositories|repositories|list|
|DELETE /api/v1/repositories|DeleteRepositories|repositories|deletecollection|
|PUT /api/v1/repositories/{name}|ReplaceRepository|repositories|replace|
|DELETE /api/v1/repositories/{name}|DeleteRepository|repositories|delete|
|POST /api/v1/resourcesyncs|CreateResourceSync|resourcesyncs|create|
|GET /api/v1/resourcesyncs|ListResourceSync|resourcesyncs|list|
|DELETE /api/v1/resourcesyncs|DeleteResourceSyncs|resourcesyncs|deletecollection|
|GET /api/v1/resourcesyncs/{name}|ReadResourceSync|resourcesyncs|read|
|PUT /api/v1/resourcesyncs/{name}|ReplaceResourceSync|resourcesyncs|replace|
|DELETE /api/v1/resourcesyncs/{name}|DeleteResourceSync|resourcesyncs|delete|
|GET /api/v1/api/v1/fleets/{fleet}/templateVersions|ListTemplateVersions|templateversions|list|
|DELETE /api/v1/api/v1/fleets/{fleet}/templateVersions|DeleteTemplateVersions|templateversions|deletecollection|
|GET /api/v1/fleets/{fleet}/templateVersions/{name}|ReadTemplateVersion|templateversions|read|
|DELETE /api/v1/fleets/{fleet}/templateVersions/{name}|DeleteTemplateVersion|templateversions|delete|
