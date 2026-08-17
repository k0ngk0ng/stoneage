#import <CoreGraphics/CoreGraphics.h>
#import <ImageIO/ImageIO.h>
#import <Foundation/Foundation.h>
#include <dlfcn.h>

int main(int argc, const char **argv) {
    if (argc != 3) { fprintf(stderr, "usage: capture-window-objc WINDOW OUTPUT.png\n"); return 2; }
    CGWindowID window = (CGWindowID)strtoul(argv[1], NULL, 10);
    CGRect bounds = CGRectNull;
    void *library = dlopen("/System/Library/Frameworks/CoreGraphics.framework/CoreGraphics", RTLD_LAZY);
    typedef CGImageRef (*CaptureFn)(CGRect, CGWindowListOption, CGWindowID, CGWindowImageOption);
    CaptureFn capture = library ? (CaptureFn)dlsym(library, "CGWindowListCreateImage") : NULL;
    CGImageRef image = capture ? capture(bounds, kCGWindowListOptionIncludingWindow,
                                          window, kCGWindowImageBoundsIgnoreFraming | kCGWindowImageBestResolution) : NULL;
    if (!image) { fprintf(stderr, "capture failed\n"); return 1; }
    NSURL *url = [NSURL fileURLWithPath:[NSString stringWithUTF8String:argv[2]]];
    CGImageDestinationRef destination = CGImageDestinationCreateWithURL((__bridge CFURLRef)url,
                                                                          CFSTR("public.png"), 1, NULL);
    if (!destination) { CGImageRelease(image); fprintf(stderr, "destination failed\n"); return 1; }
    CGImageDestinationAddImage(destination, image, NULL);
    bool ok = CGImageDestinationFinalize(destination);
    CFRelease(destination);
    CGImageRelease(image);
    if (!ok) { fprintf(stderr, "write failed\n"); return 1; }
    printf("wrote %s\n", argv[2]);
    return 0;
}
