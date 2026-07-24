// Package server assembles caller-constructed message transports and
// protocol-native runners under one validated, observable, context-aware
// lifecycle. It depends only on the shared transport contracts; applications
// select concrete protocols and attach them with Server.Mount, MountNative, or
// Server.MountRunner.
package server
