#!/bin/bash
# System uptime in human-readable format

if [ -f /proc/uptime ]; then
    uptime_seconds=$(cut -d. -f1 /proc/uptime)
    days=$((uptime_seconds / 86400))
    hours=$(((uptime_seconds % 86400) / 3600))
    minutes=$(((uptime_seconds % 3600) / 60))
    
    if [ $days -gt 0 ]; then
        echo "${days}d ${hours}h ${minutes}m"
    elif [ $hours -gt 0 ]; then
        echo "${hours}h ${minutes}m"
    else
        echo "${minutes}m"
    fi
else
    echo "unavailable"
fi
