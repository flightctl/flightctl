package v1beta1

// testFirmwareDigest is a syntactically valid sha256 digest used across the
// firmware fixtures.
const testFirmwareDigest = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// testFirmwareSpecJSON is a complete desired firmware document. Every optional
// field is set, and updater.config carries nested updater-owned records,
// numbers and booleans that only the selected updater release schema defines.
const testFirmwareSpecJSON = `{
  "retryGeneration": 1,
  "profile": "orin-example-profile",
  "updater": {
    "name": "nvidia-orin",
    "version": "0.1.0",
    "config": {
      "resources": [{"component": "system", "targetGuid": "00000000-0000-4000-8000-000000000001"}],
      "releases": [{
        "component": "system",
        "version": "36.5.2",
        "versionFormat": "nvidia-bsp-triplet",
        "profiles": ["orin-example-profile"],
        "artifact": {
          "source": {"type": "http", "repository": "firmware-artifacts", "suffix": "orin/system.Cap"},
          "digest": "` + testFirmwareDigest + `"
        }
      }],
      "overlays": [{
        "name": "camera",
        "artifactFormat": "dtb",
        "artifact": {
          "source": {"type": "http", "repository": "firmware-artifacts", "suffix": "orin/camera.dtb"},
          "digest": "` + testFirmwareDigest + `",
          "authentication": {
            "type": "cms-detached",
            "signature": {
              "source": {"type": "http", "repository": "firmware-signatures", "suffix": "orin/camera.dtb.p7s"},
              "digest": "` + testFirmwareDigest + `"
            }
          }
        }
      }],
      "maxStagingAttempts": 3,
      "verifyActivation": true
    }
  },
  "components": [
    {
      "name": "system",
      "version": {"exact": "36.5.2", "minimum": "36.4.2", "maximum": "36.5.2"},
      "artifactDigest": "` + testFirmwareDigest + `"
    },
    {
      "name": "bootloader",
      "version": {"minimum": "36.4.2", "latest": true}
    }
  ],
  "overlays": [
    {
      "name": "camera",
      "compatibleFirmware": [
        {"component": "system", "exact": "36.5.2"},
        {"component": "bootloader", "minimum": "36.4.2", "maximum": "36.5.2"}
      ]
    }
  ]
}`

// testFirmwareStatusJSON is a complete firmware status document. Every
// optional field is set, and the observation backend data carries
// updater-owned records that only the observation schema defines.
const testFirmwareStatusJSON = `{
  "support": {
    "apiVersion": 1,
    "supported": true,
    "profile": "orin-example-profile",
    "profileRevision": "r1",
    "reason": "Supported",
    "updaters": [{
      "name": "nvidia-orin",
      "version": "0.1.0",
      "buildDigest": "` + testFirmwareDigest + `",
      "protocol": 1,
      "schemaDigest": "` + testFirmwareDigest + `",
      "cached": true
    }]
  },
  "attempt": {
    "id": "attempt-1",
    "renderedVersion": "7",
    "specHash": "` + testFirmwareDigest + `",
    "retryGeneration": 1,
    "updater": {
      "name": "nvidia-orin",
      "version": "0.1.0",
      "buildDigest": "` + testFirmwareDigest + `",
      "protocol": 1,
      "schemaDigest": "` + testFirmwareDigest + `",
      "configDigest": "` + testFirmwareDigest + `",
      "manifestDigest": "` + testFirmwareDigest + `"
    },
    "phase": "Failed",
    "startedAt": "2026-11-09T10:00:00Z",
    "updatedAt": "2026-11-09T10:05:00Z",
    "changed": true,
    "error": {
      "reason": "ResourceLimitExceeded",
      "message": "the capsule exceeds the parser size budget",
      "retryable": false,
      "component": "system",
      "phase": "Preparing",
      "exceededBudget": "maxArtifactBytes"
    },
    "recovery": [{
      "code": "ReplaceArtifact",
      "message": "Supply a capsule within the parser size budget.",
      "documentationURL": "https://example.com/firmware/recovery",
      "requiresPhysicalAccess": false
    }]
  },
  "components": [
    {
      "name": "system",
      "type": "firmware",
      "managed": true,
      "state": "Failed",
      "selected": {
        "source": {"kind": "http", "identity": "firmware-artifacts/orin/system.Cap", "digest": "` + testFirmwareDigest + `"},
        "version": "36.5.2"
      },
      "actual": {"version": "36.4.4", "rawVersion": "36.4.4-gcid-1", "active": true},
      "observation": {
        "producerIdentity": "nvidia-orin/0.1.0",
        "backend": {
          "updater": "nvidia-orin",
          "schemaDigest": "` + testFirmwareDigest + `",
          "data": {
            "dmiVersion": "36.4.4-gcid-1",
            "esrt": {"resourceGuid": "00000000-0000-4000-8000-000000000001", "currentVersion": "2359554", "lastAttemptStatus": 0}
          }
        }
      },
      "observedAt": "2026-11-09T10:05:00Z",
      "bootID": "boot-1",
      "error": {"reason": "ArtifactDigestMismatch", "message": "the capsule digest does not match the requested digest", "retryable": false}
    },
    {
      "name": "camera",
      "type": "overlay",
      "managed": true,
      "state": "Pending",
      "actual": {"active": false},
      "observedAt": "2026-11-09T10:05:00Z",
      "bootID": "boot-1"
    }
  ],
  "observedAt": "2026-11-09T10:05:00Z",
  "bootID": "boot-1"
}`

// testCanceledFirmwareAttemptJSON is a terminal attempt that was superseded.
const testCanceledFirmwareAttemptJSON = `{
  "id": "attempt-2",
  "renderedVersion": "8",
  "specHash": "` + testFirmwareDigest + `",
  "retryGeneration": 0,
  "phase": "Canceled",
  "startedAt": "2026-11-09T11:00:00Z",
  "updatedAt": "2026-11-09T11:01:00Z",
  "changed": false,
  "cancellationReason": "Superseded",
  "recovery": []
}`
