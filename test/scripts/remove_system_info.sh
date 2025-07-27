cat > /tmp/remove_system_info.awk << 'AWKEOF'
BEGIN { skip = 0 }
/^system-info:/ { skip = 1; next }
/^[^ \t]/ && skip { skip = 0 }
!skip { print }
AWKEOF

awk -f /tmp/remove_system_info.awk /etc/flightctl/config.yaml > /tmp/config_clean.yaml && sudo mv /tmp/config_clean.yaml /etc/flightctl/config.yaml
rm -f /tmp/remove_system_info.awk
