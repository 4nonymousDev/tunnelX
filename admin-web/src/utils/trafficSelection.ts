// Keep exceptions by device ID so a brief disconnect does not reset a toggle.
// New devices follow the last all-on/all-off choice for their role.
export interface TrafficSelection {
  defaultEnabled: boolean
  exceptions: readonly string[]
}

export function trafficDeviceEnabled(selection: TrafficSelection, id: string): boolean {
  return selection.exceptions.includes(id) ? !selection.defaultEnabled : selection.defaultEnabled
}

export function toggleTrafficDevice(selection: TrafficSelection, id: string): TrafficSelection {
  const exceptions = new Set(selection.exceptions)
  if (exceptions.has(id)) exceptions.delete(id)
  else exceptions.add(id)
  return { defaultEnabled: selection.defaultEnabled, exceptions: [...exceptions] }
}
