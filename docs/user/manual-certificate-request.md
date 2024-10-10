# Manual creation of Certificate Signing Request for Enrollment Certificate

These steps outline how to manually create a CSR for a device's enrollment certificate. This may be useful in the case that you would like to create your own CSR with openssl and submit this to the flightctl service.

## Create the signed CSR

This CSR will be embedded in the CSR resource configuration file that will be applied to flightctl.

Create a certificate signing request `.csr` file with openssl:

```console
openssl req -new -sha256 -key myeckey.pem -out mycsr.csr
```

**NOTES**:

1. The signing key passed with `-key` MUST be an ECDSA key. To generate an ECDSA key, use `openssl ecparam -name secp521r1 -genkey -noout -out myeckey.pem` or see the [openssl documentation on ECDSA](https://docs.openssl.org/1.0.2/man1/ecparam/#synopsis) for more options.
2. The Subject Common Name in the CSR MUST be either blank or at least 16 characters in length.

For more options, including options for generating a new private key to sign the CSR, see [openssl's documentation](https://docs.openssl.org/master/man1/openssl-req/#options).

## Create the CSR resource configuration file

You can create a CSR resource configuration file wrapping the CSR file above either by generating it with flightctl or manually. Each option is described below. You may choose the CSR name, and desired expiration in seconds.

### Option 1: Generate the CSR config file

Issue the command below, specifying your `.csr` file and an output file, and optionally specifying the CSR name and expiration in seconds. The CSR name defaults to `mycsr`, the expiration to 604800 seconds. The `-y` flag enables overwriting the output file if it already exists.

```console
flightctl csr-generate mycsr.csr -e 604800 -n chosenname -o myoutputfile -y
```

### Option 2: Manually create the CSR config file

The file name, `metadata.name`, and `spec.expirationSeconds` can vary. The `apiVersion`, `kind`, `spec.signerName`, and `spec.usages` must match those below. The `spec.request` field will hold the base64-encoded contents of the `.csr` file previously created.

```console
$ cat > mycsrresource.yaml <<EOF
apiVersion: v1alpha1
kind: CertificateSigningRequest
metadata:
  name: mycsr
spec:
  request: <add base64-encoded CSR>
  signerName: enrollment
  usages: ["clientAuth", "CA:false"]
  expirationSeconds: 604800
EOF
```

Add the base64-encoded contents of the previously created CSR to the field `spec.request`, making sure to remove newlines. This can be generated with:

```console
cat mycsr.csr | base64 | tr -d '\n'
```

The end result should be structured the same way as [`examples/csr.yaml`](/examples/csr.yaml).

## Create the CSR resource in flightctl

You may then create the CSR resource by running the command below:

```console
$ flightctl apply -f mycsrresource.yaml
certificatesigningrequest: applying mycsrresource.yaml/mycsr: 201 Created
```

You can view the status of the certificate signing request with:

```console
$ flightctl get csr/mycsr
NAME    AGE     SIGNERNAME  USERNAME    REQUESTEDDURATION   CONDITION
mycsr   2m29s   enrollment  <none>      10m0s               Approved

```

The condition should show "approved," as enrollment certificates are automatically approved by the flightctl service.

## Retrieve and use the agent config file

Once the CSR has been approved, retrieve the agent configuration with enrollment credentials by running:

```console
flightctl enrollmentconfig mycsrname -p myeckey.pem
```

The returned output should look similar to this:

```console
enrollment-service:
  authentication:
    client-certificate-data: LS0tLS1CRUdJTiBD...
    client-key-data: LS0tLS1CRUdJTiBF...
  enrollment-ui-endpoint: https://ui.flightctl.127.0.0.1.nip.io:8080
  service:
    certificate-authority-data: LS0tLS1CRUdJTiBD...
    server: https://agent-api.flightctl.127.0.0.1.nip.io:7443
  grpc-management-endpoint: grpcs://agent-grpc.127.0.0.1.nip.io:7444
```
