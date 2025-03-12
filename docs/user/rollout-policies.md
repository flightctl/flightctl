# Rollout Device Selection and Rollout Disruption Budget

## Overview

When performing a rollout using `flightctl`, it is crucial to manage which devices participate in the rollout and how much disruption is acceptable. This document outlines the device selection process and the concept of the rollout disruption budget to ensure controlled and predictable rollouts.

---

## Rollout Device Selection

### **Device Targeting**

A rollout applies only to devices that belong to a fleet. Each device can belong to only a single fleet.
Since rollout definitions are done at the fleet level, the selection process determines which devices within a fleet will
participate in a batch rollout based on label criteria.  Eventually, after processing all batches, all fleet
devices should be rolled out.

- **Labels**: Devices with specific metadata labels can be targeted for rollouts.
- **Fleet Membership**: Rollouts apply only to devices within the specified fleet.

### **Device Selection Strategy**

Currently, Flightctl supports only the **BatchSequence** strategy for device selection. This strategy defines a stepwise rollout process where devices are added in batches based on specific criteria.

Batches are executed sequentially. After each batch completes, execution proceeds to the next batch only if the success rate of the previous batch meets or exceeds the configured **success threshold**. The success rate is determined as:

```text
# of successful rollouts in the batch / # of devices in the batch >= success threshold
```

In a batch sequence, the final batch is an implicit batch.  It is not specified in the batch sequence.
It selects all devices in a fleet that have not been selected by the explicit batches in the sequence.

### **Limit in Device Selection**

Each batch in the **BatchSequence** strategy may use an optional `limit` parameter to define how many devices should be included in the batch. The limit can be specified in two ways:

- **Absolute number**: A fixed number of devices to be selected.
- **Percentage**: Percentage of the total matching device population to be selected.
  - If a `selector` with labels is provided, the percentage is calculated based on the number of devices that match the label criteria within the fleet.
  - If no `selector` is provided, the percentage is applied to all devices in the fleet.

### **Success Threshold**

The **success threshold** defines the percentage of successfully updated devices required to continue the rollout. If the success rate falls below this threshold, the rollout may be paused to prevent further failures.

### **Example YAML Configuration (Fleet Spec)**

```yaml
apiVersion: v1alpha1
kind: Fleet
metadata:
  name: default
spec:
  selector:
    matchLabels:
      fleet: default
  rolloutPolicy:
    deviceSelection:
      strategy: 'BatchSequence'
      sequence:
        - selector:
            matchLabels:
              site: madrid
          limit: 1  # Absolute number
        - selector:
            matchLabels:
              site: madrid
          limit: 80%  # Percentage of devices matching the label criteria within the fleet
        - limit: 50%  # Percentage of all devices in the fleet
        - selector:
            matchLabels:
              site: paris
        - limit: 80%
        - limit: 100%
    successThreshold: 95%
```

---
In the example above, there are 6 explicit batches and 1 implicit batch:

- The first batch selects 1 device having a label **site:madrid**
- With the second batch 80% of all devices having the label **site:madrid** are either selected for rollout in the current batch or were previously selected for rollout.
- With the third batch 50% of all devices are either selected for rollout in the current batch or were previously selected for rollout.
- With the fourth batch all devices that were no previously selected and having the label **site:paris** are selected.
- With the fifth batch 80% of all devices are either selected for rollout in the current batch or were previously selected for rollout.
- With the sixth batch 100% of all devices are either selected for rollout in the current batch or were previously selected for rollout.
- The last implicit batch selects all devices that have not been selected in any previous batch (might be none).

## Rollout Disruption Budget

### **Definition**

A rollout disruption budget defines the acceptable level of service impact during a rollout. This ensures that a deployment does not take down too many devices at once, maintaining overall system stability.

### **Disruption Budget Parameters**

1. **Group By**: Defines how devices are grouped when applying the disruption budget.  The grouping is done by label keys.
2. **Min Available**: Specifies the minimum number of devices that must remain available during a rollout.
3. **Max Unavailable**: Limits the number of devices that can be unavailable at the same time.

### **Example YAML Configuration (Fleet Spec)**

```yaml
apiVersion: v1alpha1
kind: Fleet
metadata:
  name: default
spec:
  selector:
    matchLabels:
      fleet: default
  rolloutPolicy:
    disruptionBudget:
      groupBy: ['site', 'function']
      minAvailable: 1
      maxUnavailable: 10
```

---
In the example above, the grouping is performed on 2 label keys: **site** and **function**. So a group for
disruption budget consists from all devices in a fleet having identical label values for the above label keys.
For every such group the conditions defined in this specification are continuously enforced.
