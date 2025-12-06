# User Documentation

Welcome to the Flight Control user documentation.

**[Introduction](introduction.md)** - An introduction to the project, basic concepts, and Flight Control's high-level architecture.

**Installing Flight Control** - How to install the Flight Control service, agent, and CLI.

* **Deploying the Flight Control Service**

  * **[Installing the Flight Control Service on Kubernetes](installing/installing-service-on-kubernetes.md)**
    * [Installing on Kubernetes](installing/installing-service-on-kubernetes.md#installing-on-kubernetes)
    * [Installing on OpenShift](installing/installing-service-on-kubernetes.md#installing-on-openshift)
    * [Installing on Disconnected OpenShift](installing/installing-service-on-openshift-disconnected.md)
    * [Installing with Advanced Cluster Management](installing/installing-service-on-kubernetes.md#installing-with-advanced-cluster-management)
    * [Installing with Ansible Automation Platform](installing/installing-service-on-kubernetes.md#installing-with-ansible-automation-platform)

  * **[Installing the Flight Control Service on Linux](installing/installing-service-on-linux.md)**

  * Configuring the Flight Control Service
    * [Configuring Authentication and Authorization](installing/configuring-auth/overview.md)
    * [Configuring an External PostgreSQL Database](installing/configuring-external-database.md)
    * [Configuring Device Attestation](installing/configuring-device-attestation.md)
    * [Configuring Rate Limits on API Requests](installing/configuring-rate-limiting.md)

  * Monitoring the Flight Control Service
    * [Configuring Service Tracing](installing/configuring-service-tracing.md) (advanced)

  * Backing-up and Restoring the Flight Control Service
    * [Backing up the PostgreSQL Database](installing/performing-database-backup.md)
    * [Restoring from Backup](installing/performing-database-restore.md)

* **[Installing the Flight Control CLI](installing/installing-cli.md)**

* **[Installing the Flight Control Agent](installing/installing-agent.md)**

**Using Flight Control** - How to manage individual and fleets of devices with Flight Control.

* **[Using the CLI](using/cli/overview.md)** - How to use the Flight Control CLI
* **[Provisioning Devices](using/provisioning-devices.md)** - How to provision a device with an OS image.
  * [Provisioning Physical Devices](using/provisioning-devices.md#provisioning-physical-devices)
  * [Provisioning on Red Hat OpenShift Virtualization](using/provisioning-devices.md#provisioning-on-red-hat-openshift-virtualization)
  * [Provisioning on VMware vSphere](using/provisioning-devices.md#provisioning-on-vmware-vsphere)
* **[Managing Devices](using/managing-devices.md)** - How to manage individual devices.
  * [Enrolling Devices](using/managing-devices.md#enrolling-devices)
  * [Viewing the Device Inventory and Device Details](using/managing-devices.md#viewing-the-device-inventory-and-device-details)
  * [Organizing Devices](using/managing-devices.md#organizing-devices)
  * [Updating the OS](using/managing-devices.md#updating-the-os)
  * [Managing OS Configuration](using/managing-devices.md#managing-os-configuration)
  * [Managing Applications](using/managing-devices.md#managing-applications)
  * [Using Device Lifecycle Hooks](using/managing-devices.md#using-device-lifecycle-hooks)
  * [Monitoring Device Resources](using/managing-devices.md#monitoring-device-resources)
  * [Accessing Devices Remotely](using/managing-devices.md#accessing-devices-remotely)
  * [Scheduling Updates and Downloads](using/managing-devices.md#scheduling-updates-and-downloads)
  * [Troubleshooting](using/troubleshooting.md)
* **[Managing Device Fleets](using/managing-fleets.md)** - How to manage fleets of devices.
  * [Understanding Fleets](using/managing-fleets.md#understanding-fleets)
  * [Selecting Devices into a Fleet](using/managing-fleets.md#selecting-devices-into-a-fleet)
  * [Defining Device Templates](using/managing-fleets.md#defining-device-templates)
  * [Defining Rollout Policies](using/managing-fleets.md#defining-rollout-policies)
* **Solving Specific Use Cases** - How to solve specific use cases in Flight Control.
  * [Auto-Registering Devices with MicroShift into ACM](using/registering-microshift-devices-acm.md)
  * [Adding Device Observability](using/device-observability.md)

**Building for Flight Control** - How to build OS images and application packages compatible with Flight Control.

* **[Building Images](building/building-images.md)** - How to build your own OS images and publish them to a container registry.
  * [Understanding OS Images and the Image Build Process](building/building-images.md#understanding-os-images-and-the-image-build-process)
  * [Building and Publishing OS Images and Disk Images](building/building-images.md#building-and-publishing-os-images-and-disk-images)
  * [Considerations for Specific Target Platforms](building/building-images.md#considerations-for-specific-target-platforms)
    * [Red Hat OpenShift Virtualization](building/building-images.md#red-hat-openshift-virtualization)
    * [VMware vSphere](building/building-images.md#vmware-vsphere)
  * [Testing an OS image on a developer machine](using/provisioning-devices.md#testing-an-os-image-on-a-developer-machine)
  * [Best Practices](building/building-images.md#best-practices-when-building-images)
* **[Building Application Packages](building/building-images.md)** - What to know when packaging containerized applications for Flight Control.

**References** - Useful references.

* [CLI Commands](references/cli-commands.md)
* [API Resources](references/api-resources.md)
* [Authentication Resources](references/auth-resources.md)
* [Device Status Definitions](references/device-api-statuses.md)
* [Certificate Architecture](references/certificate-architecture.md)
* [Upgrade Compatibility Matrix](references/upgrade-compatibility.md)
* [Alerts and Monitoring](references/alerts.md)
* [Metrics Configuration](references/metrics.md)
* [Security Guidelines](references/security-guidelines.md)
