import io.netty.bootstrap.ServerBootstrap;
import io.netty.buffer.ByteBuf;
import io.netty.channel.ChannelHandlerContext;
import io.netty.channel.ChannelInitializer;
import io.netty.channel.ChannelOption;
import io.netty.channel.EventLoopGroup;
import io.netty.channel.SimpleChannelInboundHandler;
import io.netty.channel.nio.NioEventLoopGroup;
import io.netty.channel.socket.SocketChannel;
import io.netty.channel.socket.nio.NioServerSocketChannel;
import io.netty.handler.codec.http.HttpObjectAggregator;
import io.netty.handler.codec.http.HttpServerCodec;
import io.netty.handler.codec.http.websocketx.BinaryWebSocketFrame;
import io.netty.handler.codec.http.websocketx.WebSocketServerProtocolHandler;

public final class NettyWebSocketKProEchoServer {
    private static final int KPRO_HEADER_SIZE = 20;
    private static final int KPRO_MAGIC = 0x4b50;
    private static final int KPRO_VERSION = 1;
    private static final int MAX_FRAME_SIZE = (1 << 20) + KPRO_HEADER_SIZE;

    private NettyWebSocketKProEchoServer() {
    }

    public static void main(String[] args) throws Exception {
        int port = args.length > 0 ? Integer.parseInt(args[0]) : 18402;
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
                            channel.pipeline().addLast(new WebSocketServerProtocolHandler(
                                    "/ws", null, false, MAX_FRAME_SIZE));
                            channel.pipeline().addLast(new KProEchoHandler());
                        }
                    });
            System.out.println("netty websocket echo listening on 127.0.0.1:" + port + "/ws");
            bootstrap.bind("127.0.0.1", port).sync().channel().closeFuture().sync();
        } finally {
            boss.shutdownGracefully().sync();
            worker.shutdownGracefully().sync();
        }
    }

    private static final class KProEchoHandler extends SimpleChannelInboundHandler<BinaryWebSocketFrame> {
        @Override
        protected void channelRead0(ChannelHandlerContext ctx, BinaryWebSocketFrame frame) {
            ByteBuf input = frame.content();
            int start = input.readerIndex();
            int readable = input.readableBytes();
            if (readable < KPRO_HEADER_SIZE
                    || input.getUnsignedShort(start) != KPRO_MAGIC
                    || input.getUnsignedByte(start + 2) != KPRO_VERSION) {
                ctx.close();
                return;
            }
            long bodyLength = input.getUnsignedInt(start + 16);
            if (bodyLength > Integer.MAX_VALUE || readable != KPRO_HEADER_SIZE + (int) bodyLength) {
                ctx.close();
                return;
            }

            int messageId = input.getInt(start + 4);
            long sequence = input.getLong(start + 8);
            ByteBuf output = ctx.alloc().buffer(readable, readable);
            output.writeShort(KPRO_MAGIC);
            output.writeByte(KPRO_VERSION);
            output.writeByte(0);
            output.writeInt(messageId);
            output.writeLong(sequence);
            output.writeInt((int) bodyLength);
            output.writeBytes(input, start + KPRO_HEADER_SIZE, (int) bodyLength);
            ctx.write(new BinaryWebSocketFrame(output), ctx.voidPromise());
        }

        @Override
        public void channelReadComplete(ChannelHandlerContext ctx) {
            ctx.flush();
        }

        @Override
        public void exceptionCaught(ChannelHandlerContext ctx, Throwable cause) {
            ctx.close();
        }
    }
}
