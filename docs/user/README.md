# Flight Control User Documentation

Welcome to the Flight Control user documentation.

**[Introduction](introduction.md)** - An introduction to the project, basic concepts, and Flight Control's high-level architecture.

**Getting Started** - How to get started with Flight Control by deploying the service and enrolling your first device.

**Using Flight Control** - How to manage individual and fleets of devices with Flight Control.

* **Building Images** - How to build your own OS images and publish them to a container registry.
  * Understanding OS Images and the Build Process
  * Building and Publishing OS Images
  * Considerations for Specific Target Platforms
    * FIDO Device Onboard
    * Red Hat OpenShift Container Native Virtualization (CNV)
    * Red Hat Satellite
    * VMware vSphere
  * Best Practices
* **Provisioning Devices** - How to provision a device with an OS image.
  * Provisioning to a Physical Device
  * Provisioning to a Physical Device with FIDO Device Onboard
  * Provisioning on Red Hat OpenShift Container Native Virtualization (CNV)
  * Provisioning on Red Hat Satellite
  * Provisioning on VMware vSphere
* **Managing Devices** - How to manage individual devices.
  * Enrolling Devices
  * Organizing Devices
  * Managing Configuration
  * Managing Applications
  * Monitoring Device Resources
  * Using Device Lifecycle Hooks
  * Accessing Devices Remotely
* **Managing Fleets** - How to manage fleets of devices.
  * Understanding Fleets
  * Selecting Devices into a Fleet
  * Defining Device Templates
  * Defining Device Policies
  * Managing Fleets Using GitOps
* **Solving Specific Use Cases** - How to solve specific use cases in Flight Control.
  * Auto-Registering Devices with MicroShift into ACM
  * Adding Device Observability

**Administrating Flight Control** - How to deploy and administrate a Flight Control service.

* Installing and Configuring the Flight Control Service
* Installing and Using the Flight Control CLI
* Installing and Using the Flight Control UI
* Configuring the Flight Control Agent
* Troubleshooting

**References** - Useful references.

* [API Resources](api-resources.md)
