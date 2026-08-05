import io.netty.bootstrap.ServerBootstrap;
import io.netty.buffer.ByteBuf;
import io.netty.channel.Channel;
import io.netty.channel.ChannelHandlerContext;
import io.netty.channel.ChannelInitializer;
import io.netty.channel.ChannelOption;
import io.netty.channel.ChannelInboundHandlerAdapter;
import io.netty.channel.nio.NioEventLoopGroup;
import io.netty.channel.socket.SocketChannel;
import io.netty.channel.socket.nio.NioServerSocketChannel;
import io.netty.handler.codec.ByteToMessageDecoder;

import java.util.List;

public final class NettyKProEchoServer {
    private static final int HEADER = 20;
    private static final int MAGIC = 0x4B50;
    private static final int VERSION = 1;

    public static void main(String[] args) throws Exception {
        int port = Integer.parseInt(args.length > 0 ? args[0] : "18088");
        boolean flushEach = args.length > 1 && Boolean.parseBoolean(args[1]);
        NioEventLoopGroup boss = new NioEventLoopGroup(1);
        NioEventLoopGroup workers = new NioEventLoopGroup();
        Channel server = null;
        try {
            ServerBootstrap bootstrap = new ServerBootstrap()
                    .group(boss, workers)
                    .channel(NioServerSocketChannel.class)
                    .childOption(ChannelOption.TCP_NODELAY, true)
                    .childHandler(new ChannelInitializer<SocketChannel>() {
                        @Override
                        protected void initChannel(SocketChannel channel) {
                            channel.pipeline().addLast(new KProDecoder());
                            channel.pipeline().addLast(new EchoHandler(flushEach));
                        }
                    });
            server = bootstrap.bind(port).sync().channel();
            System.out.println("READY " + port + " flushEach=" + flushEach);
            System.out.flush();
            server.closeFuture().sync();
        } finally {
            if (server != null) {
                server.close().syncUninterruptibly();
            }
            workers.shutdownGracefully().syncUninterruptibly();
            boss.shutdownGracefully().syncUninterruptibly();
        }
    }

    private static final class KProDecoder extends ByteToMessageDecoder {
        @Override
        protected void decode(ChannelHandlerContext ctx, ByteBuf in, List<Object> out) {
            if (in.readableBytes() < HEADER) {
                return;
            }
            int index = in.readerIndex();
            if ((in.getUnsignedShort(index) != MAGIC)
                    || in.getUnsignedByte(index + 2) != VERSION) {
                ctx.close();
                return;
            }
            long length = in.getUnsignedInt(index + 16);
            if (length > 64 * 1024 || length > Integer.MAX_VALUE - HEADER) {
                ctx.close();
                return;
            }
            int frameLength = HEADER + (int) length;
            if (in.readableBytes() < frameLength) {
                return;
            }
            out.add(in.readRetainedSlice(frameLength));
        }
    }

    private static final class EchoHandler extends ChannelInboundHandlerAdapter {
        private final boolean flushEach;

        private EchoHandler(boolean flushEach) {
            this.flushEach = flushEach;
        }

        @Override
        public void channelRead(ChannelHandlerContext ctx, Object message) {
            if (flushEach) {
                ctx.writeAndFlush(message, ctx.voidPromise());
            } else {
                ctx.write(message, ctx.voidPromise());
            }
        }

        @Override
        public void channelReadComplete(ChannelHandlerContext ctx) {
            if (!flushEach) {
                ctx.flush();
            }
        }

        @Override
        public void exceptionCaught(ChannelHandlerContext ctx, Throwable cause) {
            ctx.close();
        }
    }
}
