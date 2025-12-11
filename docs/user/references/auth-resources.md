# Authentication resources

The table below contains the routes, names, resource names, and verbs for Flight Control API endpoints:

|Route| Name| Resource| Verb |
|-----|-----|---------|------|
|`GET /api/v1/certificatesigningrequests`|`ListCertificateSigningRequests`|`certificatesigningrequests`|`list`|
|`POST /api/v1/certificatesigningrequests`|`CreateCertificateSigningRequest`|`certificatesigningrequests`|`create`|
|`DELETE /api/v1/certificatesigningrequests/{name}`|`DeleteCertificateSigningRequest`|`certificatesigningrequests`|`delete`|
|`GET /api/v1/certificatesigningrequests/{name}`|`ReadCertificateSigningRequest`|`certificatesigningrequests`|`get`|
|`PATCH /api/v1/certificatesigningrequests/{name}`|`PatchCertificateSigningRequest`|`certificatesigningrequests`|`patch`|
|`PUT /api/v1/certificatesigningrequests/{name}`|`ReplaceCertificateSigningRequest`|`certificatesigningrequests`|`update`|
|`DELETE /api/v1/certificatesigningrequests/{name}/approval`|`DenyCertificateSigningRequest`|`certificatesigningrequests/approval`|`delete`|
|`POST /api/v1/devices`|`CreateDevice`|`devices`|`create`|
|`GET /api/v1/devices`|`ListDevices`|`devices`|`list`|
|`GET /api/v1/devices/{name}`|`ReadDevice`|`devices`|`get`|
|`PUT /api/v1/devices/{name}`|`ReplaceDevice`|`devices`|`update`|
|`DELETE /api/v1/devices/{name}`|`DeleteDevice`|`devices`|`delete`|
|`GET /api/v1/devices/{name}/status`|`ReadDeviceStatus`|`devices/status`|`get`|
|`PUT /api/v1/devices/{name}/status`|`ReplaceDeviceStatus`|`devices/status`|`update`|
|`GET /api/v1/devices/{name}/rendered`|`GetRenderedDevice`|`devices/rendered`|`get`|
|`GET /api/v1/devices/{name}/lastseen`|`GetDeviceLastSeen`|`devices/lastseen`|`get`|
|`PUT /api/v1/devices/{name}/decommission`|`DecommissionDevice`|`devices/decommission`|`update`|
|`GET /ws/v1/devices/{name}/console`|`DeviceConsole`|`devices/console`|`get`|
|`POST /api/v1/enrollmentrequests`|`CreateEnrollmentRequest`|`enrollmentrequests`|`create`|
|`GET /api/v1/enrollmentrequests`|`ListEnrollmentRequests`|`enrollmentrequests`|`list`|
|`GET /api/v1/enrollmentrequests/{name}`|`ReadEnrollmentRequest`|`enrollmentrequests`|`get`|
|`PUT /api/v1/enrollmentrequests/{name}`|`ReplaceEnrollmentRequest`|`enrollmentrequests`|`update`|
|`PATCH /api/v1/enrollmentrequests/{name}`|`PatchEnrollmentRequest`|`enrollmentrequests`|`patch`|
|`DELETE /api/v1/enrollmentrequests/{name}`|`DeleteEnrollmentRequest`|`enrollmentrequests`|`delete`|
|`GET /api/v1/enrollmentrequests/{name}/status`|`ReadEnrollmentRequestStatus`|`enrollmentrequests/status`|`get`|
|`PUT /api/v1/enrollmentrequests/{name}/approval`|`ApproveEnrollmentRequest`|`enrollmentrequests/approval`|`update`|
|`PUT /api/v1/enrollmentrequests/{name}/status`|`ReplaceEnrollmentRequestStatus`|`enrollmentrequests/status`|`update`|
|`POST /api/v1/fleets`|`CreateFleet`|`fleets`|`create`|
|`GET /api/v1/fleets`|`ListFleets`|`fleets`|`list`|
|`GET /api/v1/fleets/{name}`|`ReadFleet`|`fleets`|`get`|
|`PUT /api/v1/fleets/{name}`|`ReplaceFleet`|`fleets`|`update`|
|`DELETE /api/v1/fleets/{name}`|`DeleteFleet`|`fleets`|`delete`|
|`GET /api/v1/fleets/{name}/status`|`ReadFleetStatus`|`fleets/status`|`get`|
|`PUT /api/v1/fleets/{name}/status`|`ReplaceFleetStatus`|`fleets/status`|`update`|
|`POST /api/v1/repositories`|`CreateRepository`|`repositories`|`create`|
|`GET /api/v1/repositories`|`ListRepositories`|`repositories`|`list`|
|`PUT /api/v1/repositories/{name}`|`ReplaceRepository`|`repositories`|`update`|
|`DELETE /api/v1/repositories/{name}`|`DeleteRepository`|`repositories`|`delete`|
|`POST /api/v1/resourcesyncs`|`CreateResourceSync`|`resourcesyncs`|`create`|
|`GET /api/v1/resourcesyncs`|`ListResourceSync`|`resourcesyncs`|`list`|
|`GET /api/v1/resourcesyncs/{name}`|`ReadResourceSync`|`resourcesyncs`|`get`|
|`PUT /api/v1/resourcesyncs/{name}`|`ReplaceResourceSync`|`resourcesyncs`|`update`|
|`DELETE /api/v1/resourcesyncs/{name}`|`DeleteResourceSync`|`resourcesyncs`|`delete`|
|`GET /api/v1/fleets/{fleet}/templateVersions`|`ListTemplateVersions`|`fleets/templateversions`|`list`|
|`GET /api/v1/fleets/{fleet}/templateVersions/{name}`|`ReadTemplateVersion`|`fleets/templateversions`|`get`|
|`DELETE /api/v1/fleets/{fleet}/templateVersions/{name}`|`DeleteTemplateVersion`|`fleets/templateversions`|`delete`|
