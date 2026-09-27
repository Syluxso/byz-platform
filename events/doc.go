// Package events will provide Emit for the live flat camelCase Kafka envelope
// (eventId, type, occurredAt, organizationId, tenantId, userId, ...) with producer
// and traceId as additive fields. See BYZ-18.
package events
