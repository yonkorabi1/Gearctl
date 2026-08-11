import { CliDriver } from './driver.interface';
import { RawDriver } from './raw-driver';

/**
 * Resolve a driver for a given vendor hint. Phase 1 always returns
 * RawDriver regardless of `vendor` -- the field is accepted in the request
 * purely for audit-trail/forward-compat. Phase 2 adds vendor-specific
 * drivers here (e.g. `case 'huawei': return new HuaweiOltDriver();`)
 * without touching the HTTP route, transport layer, or the RawDriver
 * fallback for unrecognized/unknown vendors.
 */
export function resolveDriver(_vendor: string | undefined): CliDriver {
  return new RawDriver();
}
