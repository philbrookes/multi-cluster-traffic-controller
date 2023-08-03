# Provider agnostic DNS Health checks

## Introduction

The MGC has the ability of extending the DNS configuration of the gateway with the DNSPolicy resource. This resource allows users to configure health checks. As a result of configuring health checks, the controller creates the health checks in Route53, attaching them to the related DNS records. This has the benefit of automatically disabling an endpoint if it becomes unhealthy, and enabling it again when it becomes healthy again.
This feature has a few shortfalls:

1. It’s tightly coupled with Route53. If other DNS providers are supported they must either provide a similar feature, or health checks will not be supported
2. Lacks the ability to reach endpoints in private networks

This document describes a proposal to extend the current health check implementation to overcome these shortfalls.

### Goals

* Ability to configure health checks in the DNSPolicy associated to a Gateway
* Ability for the health checks to reach endpoints in private networks
* DNS records are disabled when the associated health check fails
* Current status of the defined health checks is visible to the end user
* ~~Transparently keep support for other health check providers like Route53~~

## Proposal

#### `DNSPolicy` resource

The presence of the `healthCheck` field in the DNSPolicy will affect the reconciliation
of the endpoints when creating/updating DNSRecords:

* For every endpoint that is generated, a health check is created based on the configuration in the DNSPolicy
* Relevant `providerSpecific` fields are added to the endpoint:
  * A reference to the equivalent `DNSHealthCheckProbe` in the downstream cluster
  will be set: `health-probe/name`, `health-probe/namespace`
  * The health status of the `DNSHealthCheckProbe` will be propagated

A `failureThreshold` field will be added to the health spec, allowing users
to configure how many consecutive health check failures are observed before
taking the unhealthy endpoint down

#### `DNSHealthCheckProbe` resource

The DNSHealthCheckProbe resource configures a health probe in the controller to perform the health checks against a local endpoint.

```yaml
apiVersion: kuadrant.io/v1alpha1
kind: DNSHealthCheckProbe
metadata:
  name: example-probe
spec:
  port: "..."
  host: “...”
  ipAddress: "..."
  path: "..."
  protocol: "..."
  interval: "..."
  AdditionalHeaders:
  - Name: "..."
    Value: "..."
  ExpectedResponses:
  - 201
    200
  - 301
  AllowInsecureCertificate: bool
status:
  healthy: true
  consecutiveFailures: 0
  reason: ""
  lastCheck: "..."
```

The reconciliation of this resource results in the configuration of a health probe,
which targets the endpoint and updates the status. The status is propagated to the providerSpecific status of the equivalent endpoint in the DNSRecord

### Changes to current controllers

In order to support this new feature, the following changes in the behaviour of the controllers are proposed.

#### DNSPolicy controller

Currently the reconciliation loop of this controller is in charge of creating health checks in the configured DNS provider (Route53 currently) based on the spec of the DNSPolicy, separately from the reconciliation of the DNSRecords. The proposed change is to reconcile health checks as the DNSRecords are created, using the health check
configuration to alter the behaviour of the DNSRecord reconciliation.

Instead of Route53 health checks, the controller will create `DNSHealthCheckProbe` resources

#### DNSRecord controller

Currently the reconciliation loop of this controller updates the DNS records using the aws/health-check-id provider specific field, that is updated as a result of the DNSPolicy reconciliation. The proposed change is to extend the functionality of acting on provider specific fields, to disable/enable the DNS record if the health-probe/healthy field is Unhealthy. This can be achieved by either deleting/re-creating the DNS record, or setting the weight to 0 in order to disable it

## DNS Record Structure Diagram:

https://lucid.app/lucidchart/2f95c9c9-8ddf-4609-af37-48145c02ef7f/edit?viewport_loc=-188%2C-61%2C2400%2C1183%2C0_0&invitationId=inv_d5f35eb7-16a9-40ec-b568-38556de9b568
How

## Removing unhealthy Endpoints
When a DNS health check probe is failing, it will update the DNS Record CR with a custom field on that endpoint to mark it as failing.

There are then 3 scenarios which we need to consider:
1 - All endpoints are healthy
2 - All endpoints are unhealthy
3 - Some endpoints are healthy and some are unhealthy.

In the cases 1 and 2, the result should be the same: All records are published to the DNS Provider.

When scenario 3 is encountered the following process should be followed:

    For each gateway IP or CNAME: this should be omitted if unhealthy.
    For each managed gateway CNAME: This should be omitted if all child records are unhealthy.
    For each GEO CNAME: This should be omitted if all the managed gateway CNAMEs have been omitted.
    Load balancer CNAME: This should never be omitted.

If we consider the DNS record to be a hierarchy of parents and children, then whenever any parent has no healthy children that parent is also considered unhealthy. No unhealthy elements are to be included in the DNS Record.
