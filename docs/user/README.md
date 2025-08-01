# Flight Control User Documentation

Welcome to the Flight Control user documentation.

**[Introduction](introduction.md)** - An introduction to the project, basic concepts, and Flight Control's high-level architecture.

**[Getting Started](getting-started.md)** - How to get started with Flight Control by deploying the service and enrolling your first device.

**Using Flight Control** - How to manage individual and fleets of devices with Flight Control.

* **[Building Images](building-images.md)** - How to build your own OS images and publish them to a container registry.
  * [Understanding OS Images and the Image Build Process](building-images.md#understanding-os-images-and-the-image-build-process)
  * [Building and Publishing OS Images and Disk Images](building-images.md#building-and-publishing-os-images-and-disk-images)
  * [Considerations for Specific Target Platforms](building-images.md#considerations-for-specific-target-platforms)
    * [Red Hat OpenShift Virtualization](building-images.md#red-hat-openshift-virtualization)
    * [VMware vSphere](building-images.md#vmware-vsphere)
  * [Best Practices](building-images.md#best-practices)
* **[Provisioning Devices](provisioning-devices.md)** - How to provision a device with an OS image.
  * [Testing an OS image on a developer machine](provisioning-devices.md#testing-an-os-image-on-a-developer-machine)
  * [Provisioning Physical Devices](provisioning-devices.md#provisioning-physical-devices)
  * [Provisioning on Red Hat OpenShift Virtualization](provisioning-devices.md#provisioning-on-red-hat-openshift-virtualization)
  * [Provisioning on VMware vSphere](provisioning-devices.md#provisioning-on-vmware-vsphere)
* **[Managing Devices](managing-devices.md)** - How to manage individual devices.
  * [Enrolling Devices](managing-devices.md#enrolling-devices)
  * [Viewing the Device Inventory and Device Details](managing-devices.md#viewing-the-device-inventory-and-device-details)
  * [Organizing Devices](managing-devices.md#organizing-devices)
  * [Updating the OS](managing-devices.md#updating-the-os)
  * [Managing OS Configuration](managing-devices.md#managing-configuration)
  * [Managing Applications](managing-devices.md#managing-applications)
  * [Using Device Lifecycle Hooks](managing-devices.md#using-device-lifecycle-hooks)
  * [Monitoring Device Resources](managing-devices.md#monitoring-device-resources)
  * [Accessing Devices Remotely](managing-devices.md#accessing-devices-remotely)
  * [Scheduling Updates and Downloads](managing-devices.md#scheduling-updates-and-downloads)
* **[Alerts and Monitoring](alerts-monitoring.md)** - How to monitor device health and manage alerts.
* **[Managing Device Fleets](managing-fleets.md)** - How to manage fleets of devices.
  * [Understanding Fleets](managing-fleets.md#understanding-fleets)
  * [Selecting Devices into a Fleet](managing-fleets.md#selecting-devices-into-a-fleet)
  * [Defining Device Templates](managing-fleets.md#defining-device-templates)
  * [Defining Rollout Policies](managing-fleets.md#defining-rollout-policies)
  * [Managing Fleets Using GitOps](managing-fleets.md#managing-fleets-using-gitops)
* **Solving Specific Use Cases** - How to solve specific use cases in Flight Control.
  * [Auto-Registering Devices with MicroShift into ACM](registering-microshift-devices-acm.md)
  * Adding Device Observability

**Administrating Flight Control** - How to deploy and administrate a Flight Control service.

* Installing and Configuring the Flight Control Service and UI
  * [Configuring Flight Control to use k8s auth](kubernetes-auth.md)
* [Installing the Flight Control CLI](install-cli.md)
* Using the Flight Control CLI
* [Configuring the Flight Control Agent](configuring-agent.md)
* [Troubleshooting](troubleshooting.md)

**References** - Useful references.

* [API Resources](api-resources.md)
* [Device Status Definitions](device-api-statuses.md)
