package main

/*
#cgo LDFLAGS: -framework IOKit -framework CoreFoundation -lobjc
#include <CoreFoundation/CoreFoundation.h>
#include <IOKit/hid/IOHIDManager.h>
#include <stdbool.h>
#include <stdint.h>
#include <stdlib.h>
#include <string.h>

void *objc_autoreleasePoolPush(void);
void objc_autoreleasePoolPop(void *);

typedef struct {
	double lux;
	int matches;
	uint32_t vendor_id, product_id, location_id;
	bool built_in;
	char product[128];
} als_reading;

static uint32_t device_id(IOHIDDeviceRef device, CFStringRef key) {
	CFTypeRef property = IOHIDDeviceGetProperty(device, key);
	int64_t id = 0;
	if (property != NULL && CFGetTypeID(property) == CFNumberGetTypeID()) {
		CFNumberGetValue(property, kCFNumberSInt64Type, &id);
	}
	return (uint32_t)id;
}

static bool device_is_built_in(IOHIDDeviceRef device) {
	CFTypeRef property = IOHIDDeviceGetProperty(device, CFSTR(kIOHIDBuiltInKey));
	double number = 0;
	if (property != NULL && CFGetTypeID(property) == CFNumberGetTypeID()) {
		CFNumberGetValue(property, kCFNumberDoubleType, &number);
	}
	return property == kCFBooleanTrue || number != 0;
}

static IOReturn read_lux(als_reading *out, const char **step) {
	// IOKit autoreleases objects, and cgo threads without a pool leak about 7 KB per call.
	void *pool = objc_autoreleasePoolPush();
	memset(out, 0, sizeof(*out));
	CFNumberRef usage_page = NULL, device_usage = NULL, element_usage = NULL;
	CFDictionaryRef device_match = NULL, element_match = NULL;
	CFSetRef devices = NULL;
	const void **device_list = NULL;
	CFArrayRef elements = NULL;
	IOHIDElementRef element = NULL;
	IOHIDValueRef value = NULL;
	bool open_attempted = false;
	IOReturn result = kIOReturnNoMemory;

	*step = "create hid manager";
	IOHIDManagerRef manager = IOHIDManagerCreate(kCFAllocatorDefault, kIOHIDOptionsTypeNone);
	if (manager == NULL) {
		goto cleanup;
	}

	*step = "create device match";
	int page = 0x20, sensor = 0x41;
	usage_page = CFNumberCreate(NULL, kCFNumberIntType, &page);
	device_usage = CFNumberCreate(NULL, kCFNumberIntType, &sensor);
	if (usage_page == NULL || device_usage == NULL) {
		goto cleanup;
	}
	const void *device_keys[] = {CFSTR(kIOHIDDeviceUsagePageKey), CFSTR(kIOHIDDeviceUsageKey)};
	const void *device_values[] = {usage_page, device_usage};
	device_match = CFDictionaryCreate(NULL, device_keys, device_values, 2,
	    &kCFTypeDictionaryKeyCallBacks, &kCFTypeDictionaryValueCallBacks);
	if (device_match == NULL) {
		goto cleanup;
	}
	IOHIDManagerSetDeviceMatching(manager, device_match);
	*step = "open hid manager";
	open_attempted = true;
	result = IOHIDManagerOpen(manager, kIOHIDOptionsTypeNone);
	if (result != kIOReturnSuccess) {
		goto cleanup;
	}

	devices = IOHIDManagerCopyDevices(manager);
	CFIndex count = devices == NULL ? 0 : CFSetGetCount(devices);
	out->matches = (int)count;
	if (count == 0) {
		*step = NULL;
		result = kIOReturnNoDevice;
		goto cleanup;
	}
	*step = "allocate device list";
	result = kIOReturnNoMemory;
	device_list = calloc(count, sizeof(*device_list));
	if (device_list == NULL) {
		goto cleanup;
	}
	CFSetGetValues(devices, device_list);
	IOHIDDeviceRef device = NULL;
	for (CFIndex i = 0; i < count; i++) {
		IOHIDDeviceRef candidate = (IOHIDDeviceRef)device_list[i];
		bool built_in = device_is_built_in(candidate);
		uint32_t location_id = device_id(candidate, CFSTR(kIOHIDLocationIDKey));
		if (device == NULL || (out->built_in && !built_in) ||
		    (built_in == out->built_in && location_id < out->location_id)) {
			device = candidate;
			out->built_in = built_in;
			out->location_id = location_id;
		}
	}
	out->vendor_id = device_id(device, CFSTR(kIOHIDVendorIDKey));
	out->product_id = device_id(device, CFSTR(kIOHIDProductIDKey));
	CFTypeRef product = IOHIDDeviceGetProperty(device, CFSTR(kIOHIDProductKey));
	if (product == NULL || CFGetTypeID(product) != CFStringGetTypeID() ||
	    !CFStringGetCString(product, out->product, sizeof(out->product), kCFStringEncodingUTF8)) {
		out->product[0] = 0;
	}

	*step = "create element match";
	int illuminance = 0x04D1;
	element_usage = CFNumberCreate(NULL, kCFNumberIntType, &illuminance);
	if (element_usage == NULL) {
		goto cleanup;
	}
	const void *element_keys[] = {CFSTR(kIOHIDElementUsagePageKey), CFSTR(kIOHIDElementUsageKey)};
	const void *element_values[] = {usage_page, element_usage};
	element_match = CFDictionaryCreate(NULL, element_keys, element_values, 2,
	    &kCFTypeDictionaryKeyCallBacks, &kCFTypeDictionaryValueCallBacks);
	if (element_match == NULL) {
		goto cleanup;
	}
	*step = "find illuminance element";
	result = kIOReturnNotFound;
	elements = IOHIDDeviceCopyMatchingElements(device, element_match, kIOHIDOptionsTypeNone);
	if (elements == NULL) {
		goto cleanup;
	}
	for (CFIndex i = 0; i < CFArrayGetCount(elements); i++) {
		IOHIDElementRef candidate = (IOHIDElementRef)CFArrayGetValueAtIndex(elements, i);
		if (IOHIDElementGetType(candidate) == kIOHIDElementTypeInput_Misc) {
			element = candidate;
			break;
		}
	}
	if (element == NULL) {
		goto cleanup;
	}

	*step = "read illuminance value";
	result = IOHIDDeviceGetValueWithOptions(device, element, &value, kIOHIDDeviceGetValueWithUpdate);
	if (result != kIOReturnSuccess) {
		goto cleanup;
	}
	if (value == NULL || IOHIDValueGetTimeStamp(value) == 0) {
		*step = "validate illuminance timestamp";
		result = kIOReturnNotReady;
		goto cleanup;
	}
	out->lux = IOHIDValueGetScaledValue(value, kIOHIDValueScaleTypeExponent);

cleanup:
	if (open_attempted) {
		IOHIDManagerClose(manager, kIOHIDOptionsTypeNone);
	}
	if (elements != NULL) CFRelease(elements);
	if (element_match != NULL) CFRelease(element_match);
	if (element_usage != NULL) CFRelease(element_usage);
	free(device_list);
	if (devices != NULL) CFRelease(devices);
	if (device_match != NULL) CFRelease(device_match);
	if (device_usage != NULL) CFRelease(device_usage);
	if (usage_page != NULL) CFRelease(usage_page);
	if (manager != NULL) CFRelease(manager);
	objc_autoreleasePoolPop(pool);
	return result;
}
*/
import "C"

import (
	"errors"
	"fmt"
)

var errNoSensor = errors.New("no sensor device")

type source struct {
	product    string
	vendorID   uint32
	productID  uint32
	locationID uint32
	builtIn    bool
}

func (s source) String() string {
	product := s.product
	if product == "" {
		product = "unknown sensor"
	}
	var builtIn string
	if s.builtIn {
		builtIn = ", built-in"
	}
	return fmt.Sprintf("%s (%04x:%04x, location 0x%08x%s)", product, s.vendorID, s.productID, s.locationID, builtIn)
}

type sensorReading struct {
	lux     float64
	source  source
	matches int
}

func readLux() (sensorReading, error) {
	var out C.als_reading
	var step *C.char
	result := C.read_lux(&out, &step)
	if result == C.kIOReturnSuccess {
		return sensorReading{
			lux: float64(out.lux),
			source: source{
				product:    C.GoString(&out.product[0]),
				vendorID:   uint32(out.vendor_id),
				productID:  uint32(out.product_id),
				locationID: uint32(out.location_id),
				builtIn:    bool(out.built_in),
			},
			matches: int(out.matches),
		}, nil
	}
	if step == nil && result == C.kIOReturnNoDevice {
		return sensorReading{}, errNoSensor
	}
	return sensorReading{}, fmt.Errorf("%s: ioreturn 0x%08x", C.GoString(step), uint32(result))
}
