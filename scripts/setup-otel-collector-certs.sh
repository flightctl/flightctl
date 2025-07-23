#!/bin/bash

# OpenTelemetry Collector Certificate Setup Script
# This script automates the manual certificate generation process for the FlightCtl OpenTelemetry collector
# It follows the exact steps documented in docs/user/otel-collector.md
# This script is idempotent and can be run multiple times safely

set -e

# Configuration
COLLECTOR_NAME="svc-otel-collector"
SIGNER_NAME="flightctl.io/server-svc"
TEMP_DIR="/tmp/otel-collector-certs"
K8S_NAMESPACE="flightctl-external"

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Function to print colored output
print_status() {
    echo -e "${GREEN}[INFO]${NC} $1"
}

print_warning() {
    echo -e "${YELLOW}[WARNING]${NC} $1"
}

print_error() {
    echo -e "${RED}[ERROR]${NC} $1"
}

print_debug() {
    echo -e "${BLUE}[DEBUG]${NC} $1"
}

# Function to check if command exists
command_exists() {
    command -v "$1" >/dev/null 2>&1
}

# Function to check if user has required permissions and tools
check_permissions() {
    if [[ ! -f "./bin/flightctl" ]]; then
        print_error "FlightCtl CLI not found at ./bin/flightctl"
        exit 1
    fi

    # Check if OpenSSL is available
    if ! command_exists openssl; then
        print_error "OpenSSL is required but not found. Please install OpenSSL first"
        exit 1
    fi

    # Check if user is authenticated
    if ! ./bin/flightctl get devices >/dev/null 2>&1; then
        print_error "Not authenticated with FlightCtl. Please run './bin/flightctl login' first"
        exit 1
    fi
}

# Function to check if kubectl is available
check_kubectl() {
    if command_exists kubectl; then
        return 0
    else
        return 1
    fi
}

# Function to check if certificates already exist and are valid
check_existing_certificates() {
    print_status "Checking for existing certificates..."
    
    if [[ -f "$TEMP_DIR/server.crt" && -f "$TEMP_DIR/server.key" && -f "$TEMP_DIR/ca.crt" ]]; then
        print_status "Certificate files found, validating..."
        
        # Check if certificates are valid
        if openssl verify -CAfile "$TEMP_DIR/ca.crt" "$TEMP_DIR/server.crt" >/dev/null 2>&1; then
            # Check certificate expiration (warn if expiring soon)
            EXPIRY=$(openssl x509 -in "$TEMP_DIR/server.crt" -noout -enddate 2>/dev/null | sed 's/notAfter=//')
            EXPIRY_EPOCH=$(date -d "$EXPIRY" +%s 2>/dev/null || echo "0")
            CURRENT_EPOCH=$(date +%s)
            DAYS_UNTIL_EXPIRY=$(( (EXPIRY_EPOCH - CURRENT_EPOCH) / 86400 ))
            
            if [[ $DAYS_UNTIL_EXPIRY -gt 7 ]]; then
                print_status "Valid certificates found with $DAYS_UNTIL_EXPIRY days until expiry"
                return 0  # Certificates are valid and not expiring soon
            else
                print_warning "Certificates found but expiring in $DAYS_UNTIL_EXPIRY days"
                return 1  # Certificates exist but are expiring soon
            fi
        else
            print_warning "Certificate files found but validation failed"
            return 1
        fi
    else
        print_status "No existing certificates found"
        return 1
    fi
}

# Function to cleanup existing CSR if it exists
cleanup_existing_csr() {
    print_status "Checking for existing CSR..."
    
    if ./bin/flightctl get csr/"${COLLECTOR_NAME}" >/dev/null 2>&1; then
        print_status "Found existing CSR, deleting..."
        ./bin/flightctl delete csr/"${COLLECTOR_NAME}" || true
        # Wait a moment for deletion to complete
        sleep 2
    else
        print_status "No existing CSR found"
    fi
}

# Function to create directories
create_directories() {
    print_status "Creating certificate directories..."
    
    mkdir -p "$TEMP_DIR"
    
    print_status "Directories created successfully"
}

# Function to generate certificates following the correct sequence
generate_certificates() {
    print_status "Generating certificates for OpenTelemetry collector..."
    
    # Step 1: Generate ECDSA private key (PEM format - matches flightctl default)
    print_status "Generating ECDSA private key..."
    openssl ecparam -genkey -name prime256v1 -out "$TEMP_DIR/${COLLECTOR_NAME}.key"
    
    print_status "Private key generated successfully"
    
    # Step 2: Create CSR using OpenSSL with DNS names and IP addresses
    print_status "Creating Certificate Signing Request with OpenSSL..."
    openssl req -new -key "$TEMP_DIR/${COLLECTOR_NAME}.key" \
        -subj "/CN=${COLLECTOR_NAME}" \
        -addext "subjectAltName=DNS:localhost,DNS:${COLLECTOR_NAME},DNS:otel-collector,DNS:flightctl-otel-collector,IP:127.0.0.1,IP:0.0.0.0,IP:10.100.102.70" \
        -out "$TEMP_DIR/${COLLECTOR_NAME}.csr"
    
    print_status "CSR created successfully"
    
    # Step 3: Create CSR YAML file for flightctl
    print_status "Creating CSR YAML file..."
    cat > "$TEMP_DIR/csr.yaml" << EOF
apiVersion: flightctl.io/v1alpha1
kind: CertificateSigningRequest
metadata:
  name: ${COLLECTOR_NAME}
spec:
  request: $(base64 -w 0 "$TEMP_DIR/${COLLECTOR_NAME}.csr")
  signerName: ${SIGNER_NAME}
  usages: ["clientAuth", "serverAuth", "CA:false"]
  expirationSeconds: 8640000
EOF
    
    print_status "CSR YAML file created successfully"
    
    # Step 4: Apply the CSR to flightctl
    print_status "Applying CSR to FlightCtl..."
    ./bin/flightctl apply -f "$TEMP_DIR/csr.yaml"
    
    print_status "CSR applied successfully"
    
    # Step 5: Approve the CSR
    print_status "Approving CSR..."
    ./bin/flightctl approve csr/"${COLLECTOR_NAME}"
    
    print_status "CSR approved successfully"
    
    # Step 6: Wait a moment for the certificate to be issued
    sleep 2
    
    # Step 7: Extract the issued certificate from the approved CSR
    print_status "Extracting issued certificate..."
    ./bin/flightctl get csr/"${COLLECTOR_NAME}" -o yaml > "$TEMP_DIR/csr.yaml"
    
    # Extract the certificate from the CSR status
    if command_exists yq; then
        yq eval '.status.certificate' "$TEMP_DIR/csr.yaml" | base64 -d > "$TEMP_DIR/${COLLECTOR_NAME}.crt"
    else
        # Fallback to grep/sed if yq is not available
        grep -A 1 "certificate:" "$TEMP_DIR/csr.yaml" | tail -1 | sed 's/^[[:space:]]*//' | base64 -d > "$TEMP_DIR/${COLLECTOR_NAME}.crt"
    fi
    
    print_status "Certificate extracted successfully"
}

# Function to extract CA certificate from enrollment config
extract_ca_certificate() {
    print_status "Extracting CA certificate from enrollment config..."
    
    # Get the enrollment config and extract CA certificate
    ./bin/flightctl enrollmentconfig > "$TEMP_DIR/enrollment-config.yaml"
    
    # Extract the CA certificate data
    if command_exists yq; then
        yq eval '.enrollment-service.service.certificate-authority-data' "$TEMP_DIR/enrollment-config.yaml" | base64 -d > "$TEMP_DIR/ca.crt"
    else
        # Fallback to grep/sed if yq is not available
        grep -A 1 "certificate-authority-data:" "$TEMP_DIR/enrollment-config.yaml" | tail -1 | sed 's/^[[:space:]]*//' | base64 -d > "$TEMP_DIR/ca.crt"
    fi
    
    print_status "CA certificate extracted successfully"
}

# Function to copy certificates (optional, for local deployment)
copy_certificates() {
    print_status "Copying certificates to final location..."
    
    # Copy certificates to the collector's certificate directory
    cp "$TEMP_DIR/${COLLECTOR_NAME}.crt" "$TEMP_DIR/server.crt"
    cp "$TEMP_DIR/${COLLECTOR_NAME}.key" "$TEMP_DIR/server.key"

    # Set proper permissions
    chmod 600 "$TEMP_DIR/server.key"
    chmod 644 "$TEMP_DIR/server.crt"
    chmod 644 "$TEMP_DIR/ca.crt"
    
    print_status "Certificates copied and permissions set"
}

# Function to setup Kubernetes certificates
setup_kubernetes_certificates() {
    if ! check_kubectl; then
        print_warning "kubectl not found, skipping Kubernetes certificate setup"
        return 0
    fi

    print_status "Setting up Kubernetes certificates..."

    # Check if we can connect to Kubernetes
    if ! kubectl cluster-info >/dev/null 2>&1; then
        print_warning "Cannot connect to Kubernetes cluster, skipping Kubernetes certificate setup"
        return 0
    fi

    # Check if we have the necessary certificate files in temp directory
    if [[ ! -f "$TEMP_DIR/ca.crt" ]] || [[ ! -f "$TEMP_DIR/${COLLECTOR_NAME}.crt" ]] || [[ ! -f "$TEMP_DIR/${COLLECTOR_NAME}.key" ]]; then
        print_error "Certificate files not found in temporary directory. Cannot deploy to Kubernetes."
        print_error "Please run the script without existing certificates to generate new ones."
        return 1
    fi

    # Use temporary files for Kubernetes deployment
    CA_CERT="$TEMP_DIR/ca.crt"
    COLLECTOR_CERT="$TEMP_DIR/${COLLECTOR_NAME}.crt"
    COLLECTOR_KEY="$TEMP_DIR/${COLLECTOR_NAME}.key"
    print_status "Using certificates from temporary directory for Kubernetes deployment"

    # Create the CA secret (as generic secret since we only have the CA certificate)
    print_status "Creating CA secret..."
    kubectl create secret generic flightctl-ca-secret \
        --from-file=ca.crt="$CA_CERT" \
        -n "$K8S_NAMESPACE" \
        --dry-run=client -o yaml | kubectl apply -f -

    # Create the collector certificate secret
    print_status "Creating collector certificate secret..."
    kubectl create secret tls otel-collector-tls \
        --cert="$COLLECTOR_CERT" \
        --key="$COLLECTOR_KEY" \
        -n "$K8S_NAMESPACE" \
        --dry-run=client -o yaml | kubectl apply -f -

    print_status "Kubernetes certificates setup completed"
    print_status "Note: You may need to restart the OpenTelemetry collector pod:"
    print_status "  kubectl delete pod -l flightctl.service=flightctl-otel-collector -n $K8S_NAMESPACE"
    print_status "  or"
    print_status "  kubectl rollout restart deployment/flightctl-otel-collector -n $K8S_NAMESPACE"
}

# Function to verify setup
verify_setup() {
    print_status "Verifying certificate setup..."
    
    # Check if certificate files exist
    if [[ ! -f "$TEMP_DIR/server.crt" ]]; then
        print_error "Server certificate file is missing"
        exit 1
    fi
    
    if [[ ! -f "$TEMP_DIR/server.key" ]]; then
        print_error "Server private key file is missing"
        exit 1
    fi
    
    if [[ ! -f "$TEMP_DIR/ca.crt" ]]; then
        print_error "CA certificate file is missing"
        exit 1
    fi
    
    # Verify certificate validity
    if ! openssl verify -CAfile "$TEMP_DIR/ca.crt" "$TEMP_DIR/server.crt" >/dev/null 2>&1; then
        print_error "Certificate validation failed"
        exit 1
    fi
    
    # Check certificate subject
    SUBJECT=$(openssl x509 -in "$TEMP_DIR/server.crt" -noout -subject 2>/dev/null | sed 's/subject=//')
    print_status "Certificate subject: $SUBJECT"
    
    # Check certificate expiration
    EXPIRY=$(openssl x509 -in "$TEMP_DIR/server.crt" -noout -enddate 2>/dev/null | sed 's/notAfter=//')
    print_status "Certificate expires: $EXPIRY"
    
    print_status "Certificate setup verified successfully"
}

# Function to cleanup temporary files
cleanup() {
    print_status "Cleaning up temporary files..."
    rm -rf "$TEMP_DIR"
    print_status "Cleanup completed"
}

# Function to display next steps
show_next_steps() {
    echo
    print_status "OpenTelemetry collector certificate setup completed successfully!"
    echo
    echo "Next steps:"
    echo "1. Restart the OpenTelemetry collector service to pick up the new certificates:"
    echo "   sudo systemctl restart flightctl-otel-collector"
    echo
    echo "2. Verify the collector is running:"
    echo "   curl http://localhost:9464/metrics"
    echo "   sudo systemctl status flightctl-otel-collector"
    echo
    echo "3. For Kubernetes deployment, see the documentation:"
    echo "   docs/user/otel-collector.md"
    echo
}

# Main execution
main() {
    echo "FlightCtl OpenTelemetry Collector Certificate Setup"
    echo "=================================================="
    echo
    echo "This script follows the manual certificate generation process documented in"
    echo "docs/user/otel-collector.md using OpenSSL and FlightCtl CLI."
    echo "This script is idempotent and can be run multiple times safely."
    echo
    
    check_permissions
    create_directories
    
    # Check if certificates already exist and are valid
    if check_existing_certificates; then
        print_status "Valid certificates already exist locally, skipping generation"
        verify_setup
    else
        # Clean up any existing CSR before creating new one
        cleanup_existing_csr
        
        generate_certificates
        extract_ca_certificate
        copy_certificates
        verify_setup
    fi
    
    # Always attempt to deploy to Kubernetes if kubectl is available
    # Note: This requires certificates to be in temp directory, so we need to regenerate if they don't exist there
    if ! check_kubectl; then
        print_warning "kubectl not found, skipping Kubernetes certificate setup"
    else
        if [[ ! -f "$TEMP_DIR/ca.crt" ]]; then
            print_status "Certificates not in temporary directory, regenerating for Kubernetes deployment"
            cleanup_existing_csr
            generate_certificates
            extract_ca_certificate
        fi
        setup_kubernetes_certificates
    fi
    
    cleanup
    show_next_steps
}

# Handle script arguments
case "${1:-}" in
    --help|-h)
        echo "Usage: $0 [OPTIONS]"
        echo
        echo "Options:"
        echo "  --help, -h              Show this help message"
        echo "  --verify                Only verify existing certificate setup"
        echo "  --force                 Force regeneration even if valid certificates exist"
        echo "  --k8s-namespace NAMESPACE  Kubernetes namespace for secrets (default: flightctl-external)"
        echo
        echo "This script sets up TLS certificates for the FlightCtl OpenTelemetry collector."
        echo "It follows the manual steps documented in docs/user/otel-collector.md:"
        echo "1. Generates ECDSA private key using OpenSSL"
        echo "2. Creates CSR using OpenSSL with proper DNS/IP extensions"
        echo "3. Creates and applies CSR YAML file to FlightCtl"
        echo "4. Approves the CSR and extracts the certificate"
        echo "5. Extracts CA certificate from enrollment config"
        echo "6. Copies certificates to proper locations (Podman/Systemd)"
        echo "7. Creates Kubernetes secrets (if kubectl is available)"
        echo ""
        echo "Note: This script only handles certificate setup. Configuration files are"
        echo "handled by the deployment packages (Podman/Systemd) or Helm charts (Kubernetes)."
        echo ""
        echo "This script is idempotent and can be run multiple times safely."
        exit 0
        ;;
    --verify)
        verify_setup
        exit 0
        ;;
    --force)
        print_status "Force flag specified, will regenerate certificates even if valid ones exist"
        check_permissions
        create_directories
        cleanup_existing_csr
        generate_certificates
        extract_ca_certificate
        copy_certificates
        verify_setup
        cleanup
        setup_kubernetes_certificates
        show_next_steps
        ;;
    --k8s-namespace)
        if [[ -z "$2" ]]; then
            print_error "Namespace name required for --k8s-namespace option"
            exit 1
        fi
        K8S_NAMESPACE="$2"
        shift 2
        main
        ;;
    "")
        main
        ;;
    *)
        print_error "Unknown option: $1"
        echo "Use --help for usage information"
        exit 1
        ;;
esac 