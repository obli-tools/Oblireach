// screencapture_darwin.m — ScreenCaptureKit wrapper (Objective-C)
// Compiled by CGo as part of the Go build.

#import <ScreenCaptureKit/ScreenCaptureKit.h>
#import <CoreMedia/CoreMedia.h>
#import <CoreVideo/CoreVideo.h>
#include <stdlib.h>
#include <string.h>

// Globals (shared with capture_darwin.go via extern)
extern int      g_mac_width;
extern int      g_mac_height;
extern int      g_mac_mon_x;
extern int      g_mac_mon_y;
extern uint32_t g_mac_display;

static unsigned char *g_mac_framebuf = NULL;
static int g_mac_framebuf_size = 0;
static dispatch_semaphore_t g_mac_sema = NULL;
static SCStream *g_mac_stream = nil;
static int g_mac_capturing = 0;

// Stream delegate
@interface OblireachStreamDelegate : NSObject <SCStreamOutput>
@end

@implementation OblireachStreamDelegate
- (void)stream:(SCStream *)stream didOutputSampleBuffer:(CMSampleBufferRef)sampleBuffer ofType:(SCStreamOutputType)type {
    if (type != SCStreamOutputTypeScreen) return;

    CVImageBufferRef imageBuffer = CMSampleBufferGetImageBuffer(sampleBuffer);
    if (!imageBuffer) return;

    CVPixelBufferLockBaseAddress(imageBuffer, kCVPixelBufferLock_ReadOnly);

    size_t width = CVPixelBufferGetWidth(imageBuffer);
    size_t height = CVPixelBufferGetHeight(imageBuffer);
    size_t bytesPerRow = CVPixelBufferGetBytesPerRow(imageBuffer);
    void *baseAddr = CVPixelBufferGetBaseAddress(imageBuffer);

    if (baseAddr && g_mac_framebuf) {
        int copyW = (int)(width < (size_t)g_mac_width ? width : (size_t)g_mac_width);
        int copyH = (int)(height < (size_t)g_mac_height ? height : (size_t)g_mac_height);
        for (int row = 0; row < copyH; row++) {
            memcpy(g_mac_framebuf + row * g_mac_width * 4,
                   (unsigned char*)baseAddr + row * bytesPerRow,
                   copyW * 4);
        }
    }

    CVPixelBufferUnlockBaseAddress(imageBuffer, kCVPixelBufferLock_ReadOnly);
    if (g_mac_sema) dispatch_semaphore_signal(g_mac_sema);
}
@end

static OblireachStreamDelegate *g_mac_delegate = nil;

int mac_sck_init(void) {
    if (g_mac_width <= 0 || g_mac_height <= 0) return -1;

    g_mac_framebuf_size = g_mac_width * g_mac_height * 4;
    g_mac_framebuf = (unsigned char*)calloc(1, g_mac_framebuf_size);
    if (!g_mac_framebuf) return -2;

    g_mac_sema = dispatch_semaphore_create(0);

    __block int initResult = 0;
    dispatch_semaphore_t setupSema = dispatch_semaphore_create(0);

    [SCShareableContent getShareableContentWithCompletionHandler:^(SCShareableContent *content, NSError *error) {
        if (error || !content) {
            initResult = -3;
            dispatch_semaphore_signal(setupSema);
            return;
        }

        SCDisplay *targetDisplay = nil;
        for (SCDisplay *d in content.displays) {
            if (d.displayID == g_mac_display) { targetDisplay = d; break; }
        }
        if (!targetDisplay && content.displays.count > 0) {
            targetDisplay = content.displays[0];
        }
        if (!targetDisplay) {
            initResult = -4;
            dispatch_semaphore_signal(setupSema);
            return;
        }

        SCContentFilter *filter = [[SCContentFilter alloc] initWithDisplay:targetDisplay excludingWindows:@[]];
        SCStreamConfiguration *config = [[SCStreamConfiguration alloc] init];
        config.width = g_mac_width;
        config.height = g_mac_height;
        config.minimumFrameInterval = CMTimeMake(1, 30);
        config.pixelFormat = kCVPixelFormatType_32BGRA;
        config.showsCursor = YES;

        g_mac_delegate = [[OblireachStreamDelegate alloc] init];
        g_mac_stream = [[SCStream alloc] initWithFilter:filter configuration:config delegate:nil];

        NSError *addErr = nil;
        [g_mac_stream addStreamOutput:g_mac_delegate type:SCStreamOutputTypeScreen sampleHandlerQueue:dispatch_get_global_queue(DISPATCH_QUEUE_PRIORITY_HIGH, 0) error:&addErr];
        if (addErr) {
            initResult = -5;
            dispatch_semaphore_signal(setupSema);
            return;
        }

        [g_mac_stream startCaptureWithCompletionHandler:^(NSError *startErr) {
            if (startErr) { initResult = -6; }
            else { g_mac_capturing = 1; }
            dispatch_semaphore_signal(setupSema);
        }];
    }];

    dispatch_semaphore_wait(setupSema, dispatch_time(DISPATCH_TIME_NOW, 10 * NSEC_PER_SEC));
    return initResult;
}

int mac_sck_capture_frame(unsigned char *out_bgra) {
    if (!g_mac_capturing || !g_mac_framebuf) return -1;
    long result = dispatch_semaphore_wait(g_mac_sema, dispatch_time(DISPATCH_TIME_NOW, 100 * NSEC_PER_MSEC));
    if (result != 0) return 1;
    memcpy(out_bgra, g_mac_framebuf, g_mac_width * g_mac_height * 4);
    return 0;
}

void mac_sck_close(void) {
    if (g_mac_stream && g_mac_capturing) {
        dispatch_semaphore_t stopSema = dispatch_semaphore_create(0);
        [g_mac_stream stopCaptureWithCompletionHandler:^(NSError *err) {
            dispatch_semaphore_signal(stopSema);
        }];
        dispatch_semaphore_wait(stopSema, dispatch_time(DISPATCH_TIME_NOW, 5 * NSEC_PER_SEC));
    }
    g_mac_stream = nil;
    g_mac_delegate = nil;
    g_mac_capturing = 0;
    if (g_mac_framebuf) { free(g_mac_framebuf); g_mac_framebuf = NULL; }
}
