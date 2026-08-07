import io.netty.bootstrap.ServerBootstrap;
import io.netty.buffer.ByteBuf;
import io.netty.buffer.Unpooled;
import io.netty.channel.ChannelFutureListener;
import io.netty.channel.ChannelHandlerContext;
import io.netty.channel.ChannelInitializer;
import io.netty.channel.ChannelOption;
import io.netty.channel.EventLoopGroup;
import io.netty.channel.SimpleChannelInboundHandler;
import io.netty.channel.nio.NioEventLoopGroup;
import io.netty.channel.socket.SocketChannel;
import io.netty.channel.socket.nio.NioServerSocketChannel;
import io.netty.handler.codec.http.DefaultFullHttpResponse;
import io.netty.handler.codec.http.DefaultHttpContent;
import io.netty.handler.codec.http.DefaultHttpResponse;
import io.netty.handler.codec.http.FullHttpRequest;
import io.netty.handler.codec.http.HttpHeaderNames;
import io.netty.handler.codec.http.HttpHeaderValues;
import io.netty.handler.codec.http.HttpMethod;
import io.netty.handler.codec.http.HttpObjectAggregator;
import io.netty.handler.codec.http.HttpResponse;
import io.netty.handler.codec.http.HttpResponseStatus;
import io.netty.handler.codec.http.HttpServerCodec;
import io.netty.handler.codec.http.HttpUtil;
import io.netty.handler.codec.http.HttpVersion;
import io.netty.handler.codec.http.QueryStringDecoder;

import java.nio.charset.StandardCharsets;
import java.util.List;
import java.util.Map;
import java.util.concurrent.TimeUnit;

public final class NettySSEServer {
    private static final byte[] EVENT_PREFIX = "event: message\ndata: ".getBytes(StandardCharsets.US_ASCII);
    private static final byte[] EVENT_SUFFIX = "\n\n".getBytes(StandardCharsets.US_ASCII);
    private static final byte[] HEX = "0123456789abcdef".getBytes(StandardCharsets.US_ASCII);
    private static final int SEND_QUANTUM = 32;

    private NettySSEServer() {
    }

    public static void main(String[] args) throws Exception {
        int port = args.length > 0 ? Integer.parseInt(args[0]) : 18502;
        int workers = args.length > 1 ? Integer.parseInt(args[1]) : 8;
        EventLoopGroup boss = new NioEventLoopGroup(1);
        EventLoopGroup worker = new NioEventLoopGroup(workers);
        try {
            ServerBootstrap bootstrap = new ServerBootstrap();
            bootstrap.group(boss, worker)
                    .channel(NioServerSocketChannel.class)
                    .option(ChannelOption.SO_REUSEADDR, true)
                    .childOption(ChannelOption.TCP_NODELAY, true)
                    .childOption(ChannelOption.SO_KEEPALIVE, true)
                    .childHandler(new ChannelInitializer<SocketChannel>() {
                        @Override
                        protected void initChannel(SocketChannel channel) {
                            channel.pipeline().addLast(new HttpServerCodec());
                            channel.pipeline().addLast(new HttpObjectAggregator(16 * 1024));
                            channel.pipeline().addLast(new SSEHandler());
                        }
                    });
            System.out.println("netty SSE benchmark listening on http://127.0.0.1:" + port + "/events");
            bootstrap.bind("127.0.0.1", port).sync().channel().closeFuture().sync();
        } finally {
            boss.shutdownGracefully().sync();
            worker.shutdownGracefully().sync();
        }
    }

    private static final class SSEHandler extends SimpleChannelInboundHandler<FullHttpRequest> {
        private boolean configured;
        private boolean started;
        private boolean scheduled;
        private int payloadSize;
        private int streamId;
        private long sequence;

        @Override
        protected void channelRead0(ChannelHandlerContext ctx, FullHttpRequest request) {
            if (configured || request.method() != HttpMethod.GET) {
                reject(ctx, HttpResponseStatus.BAD_REQUEST);
                return;
            }
            QueryStringDecoder query = new QueryStringDecoder(request.uri());
            if (!"/events".equals(query.path())) {
                reject(ctx, HttpResponseStatus.NOT_FOUND);
                return;
            }
            try {
                Map<String, List<String>> parameters = query.parameters();
                payloadSize = parseInt(parameters, "payload", 16, 64 * 1024);
                streamId = parseInt(parameters, "stream", 0, 1 << 30);
                long startNanos = parseLong(parameters, "start_ns");
                configured = true;

                HttpResponse response = new DefaultHttpResponse(HttpVersion.HTTP_1_1, HttpResponseStatus.OK);
                response.headers().set(HttpHeaderNames.CONTENT_TYPE, "text/event-stream");
                response.headers().set(HttpHeaderNames.CACHE_CONTROL, "no-cache");
                response.headers().set(HttpHeaderNames.CONNECTION, HttpHeaderValues.KEEP_ALIVE);
                HttpUtil.setTransferEncodingChunked(response, true);
                ctx.writeAndFlush(response, ctx.voidPromise());

                long nowNanos = System.currentTimeMillis() * 1_000_000L;
                long delayNanos = Math.max(0L, startNanos - nowNanos);
                ctx.executor().schedule(() -> {
                    started = true;
                    schedule(ctx);
                }, delayNanos, TimeUnit.NANOSECONDS);
            } catch (RuntimeException error) {
                reject(ctx, HttpResponseStatus.BAD_REQUEST);
            }
        }

        @Override
        public void channelWritabilityChanged(ChannelHandlerContext ctx) {
            if (ctx.channel().isWritable()) {
                schedule(ctx);
            }
            ctx.fireChannelWritabilityChanged();
        }

        private void schedule(ChannelHandlerContext ctx) {
            if (!started || scheduled || !ctx.channel().isActive() || !ctx.channel().isWritable()) {
                return;
            }
            scheduled = true;
            ctx.executor().execute(() -> {
                scheduled = false;
                produce(ctx);
            });
        }

        private void produce(ChannelHandlerContext ctx) {
            for (int count = 0; count < SEND_QUANTUM && ctx.channel().isActive() && ctx.channel().isWritable(); count++) {
                sequence++;
                int wireSize = EVENT_PREFIX.length + payloadSize + EVENT_SUFFIX.length;
                ByteBuf content = ctx.alloc().buffer(wireSize, wireSize);
                content.writeBytes(EVENT_PREFIX);
                writeHexSequence(content, sequence);
                for (int index = 16; index < payloadSize; index++) {
                    content.writeByte('a' + (index + streamId) % 26);
                }
                content.writeBytes(EVENT_SUFFIX);
                ctx.writeAndFlush(new DefaultHttpContent(content), ctx.voidPromise());
            }
            schedule(ctx);
        }

        private static void writeHexSequence(ByteBuf output, long value) {
            for (int shift = 60; shift >= 0; shift -= 4) {
                output.writeByte(HEX[(int) ((value >>> shift) & 0x0f)]);
            }
        }

        private static int parseInt(Map<String, List<String>> parameters, String name, int minimum, int maximum) {
            int value = Integer.parseInt(first(parameters, name));
            if (value < minimum || value > maximum) {
                throw new IllegalArgumentException(name);
            }
            return value;
        }

        private static long parseLong(Map<String, List<String>> parameters, String name) {
            return Long.parseLong(first(parameters, name));
        }

        private static String first(Map<String, List<String>> parameters, String name) {
            List<String> values = parameters.get(name);
            if (values == null || values.size() != 1) {
                throw new IllegalArgumentException(name);
            }
            return values.get(0);
        }

        private static void reject(ChannelHandlerContext ctx, HttpResponseStatus status) {
            DefaultFullHttpResponse response = new DefaultFullHttpResponse(
                    HttpVersion.HTTP_1_1, status, Unpooled.EMPTY_BUFFER);
            response.headers().setInt(HttpHeaderNames.CONTENT_LENGTH, 0);
            ctx.writeAndFlush(response).addListener(ChannelFutureListener.CLOSE);
        }

        @Override
        public void exceptionCaught(ChannelHandlerContext ctx, Throwable cause) {
            ctx.close();
        }
    }
}
